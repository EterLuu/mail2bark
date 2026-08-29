package mail2bark

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type API struct {
	Store           *Store
	AdminKey        string
	RecipientDomain string
	BasePath        string
	Log             interface{}
}

//go:embed web/*
var webFiles embed.FS

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(webFiles, "web")
	mux.Handle("/", http.FileServer(http.FS(static)))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { jsonResponse(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("/readyz", a.ready)
	mux.HandleFunc("/v1/smtp/credentials", a.credentials)
	mux.HandleFunc("/v1/smtp/credentials/", a.credentialAction)
	mux.HandleFunc("/v1/destinations", a.destinations)
	mux.HandleFunc("/v1/destinations/", a.destinationAction)
	mux.HandleFunc("/v1/messages", a.messages)
	mux.HandleFunc("/v1/messages/", a.messageAction)

	basePath := normalizeBasePath(a.BasePath)
	if basePath == "" {
		return mux
	}

	prefixed := http.StripPrefix(basePath, mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == basePath {
			target := basePath + "/"
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusPermanentRedirect)
			return
		}
		if strings.HasPrefix(r.URL.Path, basePath+"/") {
			prefixed.ServeHTTP(w, r)
			return
		}
		// Keep root routes available for proxies that strip the configured
		// prefix and for container-local health checks.
		mux.ServeHTTP(w, r)
	})
}
func (a *API) authorized(r *http.Request) bool {
	if a.AdminKey == "" {
		return true
	}
	v := r.Header.Get("Authorization")
	return strings.TrimSpace(strings.TrimPrefix(v, "Bearer ")) == a.AdminKey
}
func (a *API) guard(w http.ResponseWriter, r *http.Request) bool {
	if !a.authorized(r) {
		jsonResponse(w, 401, map[string]string{"error": "请提供有效的管理员密钥"})
		return false
	}
	return true
}

func normalizeIPs(values []string) ([]string, error) {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if strings.Contains(value, "/") {
			if _, _, err := net.ParseCIDR(value); err != nil {
				return nil, fmt.Errorf("来源 IP 格式无效：%s", value)
			}
			out = append(out, value)
			continue
		}
		ip := net.ParseIP(value)
		if ip == nil {
			return nil, fmt.Errorf("来源 IP 格式无效：%s", value)
		}
		if v4 := ip.To4(); v4 != nil {
			out = append(out, v4.String()+"/32")
		} else {
			out = append(out, ip.String()+"/128")
		}
	}
	return out, nil
}
func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.db.PingContext(r.Context()); err != nil {
		jsonResponse(w, 503, map[string]string{"status": "not_ready"})
		return
	}
	jsonResponse(w, 200, map[string]string{"status": "ready"})
}
func (a *API) credentials(w http.ResponseWriter, r *http.Request) {
	if !a.guard(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		out, err := a.Store.ListCredentials(r.Context())
		if err != nil {
			jsonResponse(w, 500, map[string]string{"error": err.Error()})
			return
		}
		jsonResponse(w, 200, out)
	case http.MethodPost:
		var input credentialInput
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "请求内容无效"})
			return
		}
		input.Enabled = true
		if err := a.validateCredentialInput(r.Context(), &input); err != nil {
			jsonResponse(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		c, p, err := a.Store.CreateCredential(r.Context(), input.Name, a.RecipientDomain, input.AllowedIPs, input.DestinationID)
		if err != nil {
			jsonResponse(w, 400, map[string]string{"error": err.Error()})
			return
		}
		jsonResponse(w, 201, map[string]any{"credential": c, "password": p})
	default:
		w.WriteHeader(405)
	}
}

type credentialInput struct {
	Name          string   `json:"name"`
	AllowedIPs    []string `json:"allowed_ips"`
	DestinationID int64    `json:"destination_id"`
	Enabled       bool     `json:"enabled"`
}

func (a *API) validateCredentialInput(ctx context.Context, input *credentialInput) error {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return fmt.Errorf("请填写来源名称")
	}
	if len(input.AllowedIPs) == 0 {
		return fmt.Errorf("至少填写一个允许的来源 IP")
	}
	ips, err := normalizeIPs(input.AllowedIPs)
	if err != nil {
		return err
	}
	input.AllowedIPs = ips
	if input.DestinationID != 0 {
		exists, err := a.Store.DestinationExists(ctx, input.DestinationID)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("指定的 Bark 设备不存在")
		}
	}
	return nil
}

func resourceAction(path, prefix string) (int64, string, bool) {
	tail := strings.Trim(strings.TrimPrefix(path, prefix), "/")
	parts := strings.Split(tail, "/")
	if tail == "" || len(parts) > 2 {
		return 0, "", false
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		return 0, "", false
	}
	if len(parts) == 2 {
		return id, parts[1], true
	}
	return id, "", true
}

func (a *API) credentialAction(w http.ResponseWriter, r *http.Request) {
	if !a.guard(w, r) {
		return
	}
	id, action, ok := resourceAction(r.URL.Path, "/v1/smtp/credentials/")
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if action == "rotate" {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		credential, secret, err := a.Store.RotateCredentialSecret(r.Context(), id)
		if err != nil {
			a.storeError(w, err)
			return
		}
		jsonResponse(w, http.StatusOK, map[string]any{"credential": credential, "password": secret})
		return
	}
	if action == "test" {
		a.smtpTest(w, r, id)
		return
	}
	if action != "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodPut:
		var input credentialInput
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "请求内容无效"})
			return
		}
		if err := a.validateCredentialInput(r.Context(), &input); err != nil {
			jsonResponse(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		credential, err := a.Store.UpdateCredential(r.Context(), id, input.Name, input.AllowedIPs, input.DestinationID, input.Enabled)
		if err != nil {
			a.storeError(w, err)
			return
		}
		jsonResponse(w, http.StatusOK, credential)
	case http.MethodDelete:
		if err := a.Store.DeleteCredential(r.Context(), id); err != nil {
			a.storeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (a *API) smtpTest(w http.ResponseWriter, r *http.Request, credentialID int64) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var input struct {
		Password string `json:"password"`
		Subject  string `json:"subject"`
		Body     string `json:"body"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil || strings.TrimSpace(input.Password) == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "请填写 SMTP API Key"})
		return
	}
	credential, err := a.Store.AuthenticateCredentialSecret(r.Context(), credentialID, input.Password)
	if err != nil {
		if errors.Is(err, errUnauthorized) {
			jsonResponse(w, http.StatusUnauthorized, map[string]string{"error": "SMTP API Key 无效或已停用"})
			return
		}
		a.storeError(w, err)
		return
	}
	if len(credential.Recipients) == 0 {
		jsonResponse(w, http.StatusConflict, map[string]string{"error": "该 Key 没有可用收件地址"})
		return
	}
	subject := strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(input.Subject))
	if subject == "" {
		subject = "mail2bark SMTP 测试"
	}
	body := strings.TrimSpace(input.Body)
	if body == "" {
		body = "这是一封由 mail2bark 管理界面生成的 SMTP 测试邮件。"
	}
	recipient := credential.Recipients[0]
	from := "mail2bark-test@localhost"
	raw := []byte(fmt.Sprintf("From: mail2bark SMTP Test <%s>\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n%s\r\n",
		from, recipient, mime.QEncoding.Encode("UTF-8", subject), time.Now().Format(time.RFC1123Z), body))
	messageID, err := a.Store.AddMessage(r.Context(), from, recipient, credential.ID, raw, subject, "mail2bark SMTP Test")
	if err != nil {
		a.storeError(w, err)
		return
	}
	jsonResponse(w, http.StatusAccepted, map[string]any{"message_id": messageID, "status": "pending"})
}
func (a *API) destinations(w http.ResponseWriter, r *http.Request) {
	if !a.guard(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		o, e := a.Store.ListDestinations(r.Context())
		if e != nil {
			jsonResponse(w, 500, map[string]string{"error": e.Error()})
			return
		}
		for i := range o {
			o[i].DeviceKey = ""
		}
		jsonResponse(w, 200, o)
	case http.MethodPost:
		var d Destination
		if json.NewDecoder(r.Body).Decode(&d) != nil {
			jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "请求内容无效"})
			return
		}
		if err := validateDestinationInput(&d, true); err != nil {
			jsonResponse(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		created, e := a.Store.CreateDestination(r.Context(), d)
		if e != nil {
			jsonResponse(w, 400, map[string]string{"error": e.Error()})
			return
		}
		jsonResponse(w, 201, map[string]any{"id": created.ID, "name": created.Name})
	default:
		w.WriteHeader(405)
	}
}

func validateDestinationInput(destination *Destination, requireDeviceKey bool) error {
	destination.Name = strings.TrimSpace(destination.Name)
	destination.Server = strings.TrimSpace(destination.Server)
	destination.DeviceKey = strings.TrimSpace(destination.DeviceKey)
	destination.Group = strings.TrimSpace(destination.Group)
	destination.Sound = strings.TrimSpace(destination.Sound)
	destination.Level = strings.TrimSpace(destination.Level)
	if destination.Name == "" || destination.Server == "" || (requireDeviceKey && destination.DeviceKey == "") {
		return fmt.Errorf("请填写设备名称、Bark 服务器和 Device Key")
	}
	endpoint, err := url.Parse(destination.Server)
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Hostname() == "" {
		return fmt.Errorf("Bark 服务器必须是有效的 HTTP 或 HTTPS 地址")
	}
	return nil
}

func (a *API) destinationAction(w http.ResponseWriter, r *http.Request) {
	if !a.guard(w, r) {
		return
	}
	id, action, ok := resourceAction(r.URL.Path, "/v1/destinations/")
	if !ok || action != "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodPut:
		var destination Destination
		if json.NewDecoder(r.Body).Decode(&destination) != nil {
			jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "请求内容无效"})
			return
		}
		if err := validateDestinationInput(&destination, false); err != nil {
			jsonResponse(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		updated, err := a.Store.UpdateDestination(r.Context(), id, destination)
		if err != nil {
			a.storeError(w, err)
			return
		}
		updated.DeviceKey = ""
		jsonResponse(w, http.StatusOK, updated)
	case http.MethodDelete:
		if err := a.Store.DeleteDestination(r.Context(), id); err != nil {
			a.storeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (a *API) storeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errNotFound):
		jsonResponse(w, http.StatusNotFound, map[string]string{"error": "资源不存在"})
	case errors.Is(err, errCredentialPending):
		jsonResponse(w, http.StatusConflict, map[string]string{"error": "该 Key 仍有待处理邮件，暂时不能删除"})
	case errors.Is(err, errDestinationInUse):
		jsonResponse(w, http.StatusConflict, map[string]string{"error": "该 Bark 设备仍被接入 Key 使用，请先修改绑定"})
	default:
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
}
func (a *API) messages(w http.ResponseWriter, r *http.Request) {
	if !a.guard(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(405)
		return
	}
	o, e := a.Store.ListMessages(r.Context())
	if e != nil {
		jsonResponse(w, 500, map[string]string{"error": e.Error()})
		return
	}
	jsonResponse(w, 200, o)
}
func (a *API) messageAction(w http.ResponseWriter, r *http.Request) {
	if !a.guard(w, r) {
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 4 || parts[0] != "v1" || parts[1] != "messages" || parts[3] != "retry" {
		w.WriteHeader(404)
		return
	}
	id, e := strconv.ParseInt(parts[2], 10, 64)
	if e != nil {
		w.WriteHeader(400)
		return
	}
	if r.Method == http.MethodPost {
		if e = a.Store.RetryMessage(r.Context(), id); e != nil {
			jsonResponse(w, 400, map[string]string{"error": e.Error()})
			return
		}
		jsonResponse(w, 202, map[string]string{"status": "retrying"})
		return
	}
	w.WriteHeader(404)
}
