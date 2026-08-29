package mail2bark

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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
	mux.HandleFunc("/v1/destinations", a.destinations)
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
		var in struct {
			Name          string   `json:"name"`
			AllowedIPs    []string `json:"allowed_ips"`
			DestinationID int64    `json:"destination_id"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil || strings.TrimSpace(in.Name) == "" {
			jsonResponse(w, 400, map[string]string{"error": "请填写来源名称"})
			return
		}
		in.Name = strings.TrimSpace(in.Name)
		if len(in.AllowedIPs) == 0 {
			jsonResponse(w, 400, map[string]string{"error": "至少填写一个允许的来源 IP"})
			return
		}
		ips, err := normalizeIPs(in.AllowedIPs)
		if err != nil {
			jsonResponse(w, 400, map[string]string{"error": err.Error()})
			return
		}
		if in.DestinationID != 0 {
			var enabled int
			if queryErr := a.Store.db.QueryRowContext(r.Context(), `SELECT enabled FROM destinations WHERE id=?`, in.DestinationID).Scan(&enabled); queryErr != nil || enabled == 0 {
				jsonResponse(w, 400, map[string]string{"error": "指定的 Bark 设备不存在或已停用"})
				return
			}
		}
		c, p, err := a.Store.CreateCredential(r.Context(), in.Name, a.RecipientDomain, ips, in.DestinationID)
		if err != nil {
			jsonResponse(w, 400, map[string]string{"error": err.Error()})
			return
		}
		jsonResponse(w, 201, map[string]any{"credential": c, "password": p})
	default:
		w.WriteHeader(405)
	}
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
		jsonResponse(w, 200, o)
	case http.MethodPost:
		var d Destination
		if json.NewDecoder(r.Body).Decode(&d) != nil || strings.TrimSpace(d.Name) == "" || strings.TrimSpace(d.Server) == "" || strings.TrimSpace(d.DeviceKey) == "" {
			jsonResponse(w, 400, map[string]string{"error": "请填写设备名称、Bark 服务器和 Device Key"})
			return
		}
		endpoint, parseErr := url.Parse(strings.TrimSpace(d.Server))
		if parseErr != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Hostname() == "" {
			jsonResponse(w, 400, map[string]string{"error": "Bark 服务器必须是有效的 HTTP 或 HTTPS 地址"})
			return
		}
		d.Name, d.Server, d.DeviceKey = strings.TrimSpace(d.Name), strings.TrimSpace(d.Server), strings.TrimSpace(d.DeviceKey)
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
