package mail2bark

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	store := NewStore(db)
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return store
}

func jsonRequest(handler http.Handler, method, target, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestNormalizeBasePath(t *testing.T) {
	tests := map[string]string{
		"":                     "",
		"/":                    "",
		"mail2bark":            "/mail2bark",
		"/mail2bark/":          "/mail2bark",
		" /apps/mail2bark/// ": "/apps/mail2bark",
	}
	for input, expected := range tests {
		if actual := normalizeBasePath(input); actual != expected {
			t.Errorf("normalizeBasePath(%q) = %q, want %q", input, actual, expected)
		}
	}
}

func TestHandlerServesConfiguredBasePath(t *testing.T) {
	handler := (&API{BasePath: "/mail2bark"}).Handler()

	t.Run("redirects base path to trailing slash", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/mail2bark?tab=messages", nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusPermanentRedirect {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusPermanentRedirect)
		}
		if location := response.Header().Get("Location"); location != "/mail2bark/?tab=messages" {
			t.Fatalf("location = %q", location)
		}
	})

	t.Run("serves UI and relative assets", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/mail2bark/", nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
		}
		body := response.Body.String()
		if !strings.Contains(body, `href="styles.css"`) || !strings.Contains(body, `src="app.js"`) {
			t.Fatal("UI assets are not relative to the configured base path")
		}
	})

	t.Run("serves root UI for prefix-stripping proxies", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
		}
	})

	t.Run("serves prefixed and root health checks", func(t *testing.T) {
		for _, target := range []string{"/mail2bark/healthz", "/healthz"} {
			request := httptest.NewRequest(http.MethodGet, target, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Errorf("GET %s returned %d", target, response.Code)
			}
		}
	})
}

func TestStoreManagementLifecycle(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	destination, err := store.CreateDestination(ctx, Destination{
		Name: "Phone", Server: "https://api.day.app", DeviceKey: "device-secret", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	credential, oldSecret, err := store.CreateCredential(ctx, "server", "notify.internal", []string{"192.0.2.1/32"}, destination.ID)
	if err != nil {
		t.Fatal(err)
	}

	updated, err := store.UpdateCredential(ctx, credential.ID, "server-updated", []string{"198.51.100.0/24"}, destination.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "server-updated" || updated.Enabled || updated.AllowedIPs[0] != "198.51.100.0/24" {
		t.Fatalf("unexpected credential update: %+v", updated)
	}
	if _, err := store.AuthenticateCredentialSecret(ctx, credential.ID, oldSecret); !errors.Is(err, errUnauthorized) {
		t.Fatalf("disabled credential authentication error = %v", err)
	}
	if _, err := store.UpdateCredential(ctx, credential.ID, updated.Name, updated.AllowedIPs, destination.ID, true); err != nil {
		t.Fatal(err)
	}

	rotated, newSecret, err := store.RotateCredentialSecret(ctx, credential.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.ID != credential.ID || newSecret == oldSecret {
		t.Fatal("credential secret was not rotated")
	}
	if _, err := store.AuthenticateCredentialSecret(ctx, credential.ID, oldSecret); !errors.Is(err, errUnauthorized) {
		t.Fatalf("old credential secret authentication error = %v", err)
	}
	if _, err := store.AuthenticateCredentialSecret(ctx, credential.ID, newSecret); err != nil {
		t.Fatal(err)
	}

	destination.Enabled = false
	destination.DeviceKey = ""
	if _, err := store.UpdateDestination(ctx, destination.ID, destination); err != nil {
		t.Fatal(err)
	}
	var storedDeviceKey string
	if err := store.db.QueryRowContext(ctx, `SELECT device_key FROM destinations WHERE id=?`, destination.ID).Scan(&storedDeviceKey); err != nil {
		t.Fatal(err)
	}
	if storedDeviceKey != "device-secret" {
		t.Fatalf("blank update replaced Device Key with %q", storedDeviceKey)
	}
	if destinations, err := store.ListDestinations(ctx); err != nil || len(destinations) != 1 || destinations[0].Enabled {
		t.Fatalf("disabled destination not listed: %+v, %v", destinations, err)
	}
	if err := store.DeleteDestination(ctx, destination.ID); !errors.Is(err, errDestinationInUse) {
		t.Fatalf("referenced destination deletion error = %v", err)
	}

	messageID, err := store.AddMessage(ctx, "test@example.com", credential.Recipients[0], credential.ID, []byte("Subject: test\r\n\r\ntest"), "test", "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteCredential(ctx, credential.ID); !errors.Is(err, errCredentialPending) {
		t.Fatalf("credential with pending message deletion error = %v", err)
	}
	store.MarkDelivered(ctx, messageID)
	if err := store.DeleteCredential(ctx, credential.ID); err != nil {
		t.Fatal(err)
	}
	if store.IsRecipient(ctx, credential.Recipients[0]) {
		t.Fatal("deleted credential recipient remains enabled")
	}
	if err := store.DeleteDestination(ctx, destination.ID); err != nil {
		t.Fatal(err)
	}
}

func TestManagementAPIAndSMTPTest(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	destination, err := store.CreateDestination(ctx, Destination{
		Name: "Phone", Server: "https://api.day.app", DeviceKey: "never-return-this", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	credential, secret, err := store.CreateCredential(ctx, "server", "notify.internal", []string{"192.0.2.1/32"}, destination.ID)
	if err != nil {
		t.Fatal(err)
	}
	handler := (&API{Store: store, RecipientDomain: "notify.internal", BasePath: "/mail2bark"}).Handler()

	response := jsonRequest(handler, http.MethodGet, "/mail2bark/v1/destinations", "")
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "never-return-this") || strings.Contains(response.Body.String(), "device_key") {
		t.Fatalf("destination list leaked Device Key: status=%d body=%s", response.Code, response.Body.String())
	}

	response = jsonRequest(handler, http.MethodPut, "/mail2bark/v1/smtp/credentials/"+strconv.FormatInt(credential.ID, 10),
		`{"name":"renamed","allowed_ips":[],"destination_id":`+strconv.FormatInt(destination.ID, 10)+`,"enabled":true}`)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"0.0.0.0/0"`) {
		t.Fatalf("credential update failed: status=%d body=%s", response.Code, response.Body.String())
	}
	response = jsonRequest(handler, http.MethodGet, "/mail2bark/v1/smtp/credentials/"+strconv.FormatInt(credential.ID, 10), "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"password":"`) || !strings.Contains(response.Body.String(), credential.Recipients[0]) {
		t.Fatalf("credential detail cannot be viewed: status=%d body=%s", response.Code, response.Body.String())
	}

	response = jsonRequest(handler, http.MethodPost, "/mail2bark/v1/smtp/credentials/"+strconv.FormatInt(credential.ID, 10)+"/rotate", "{}")
	if response.Code != http.StatusOK {
		t.Fatalf("credential rotation failed: status=%d body=%s", response.Code, response.Body.String())
	}
	var rotation struct {
		Password string `json:"password"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &rotation); err != nil || rotation.Password == "" || rotation.Password == secret {
		t.Fatalf("invalid rotation response: %s", response.Body.String())
	}

	response = jsonRequest(handler, http.MethodPost, "/mail2bark/v1/smtp/credentials/"+strconv.FormatInt(credential.ID, 10)+"/test",
		`{"password":"`+secret+`","subject":"test","body":"test"}`)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("old SMTP API Key test status = %d", response.Code)
	}
	response = jsonRequest(handler, http.MethodPost, "/mail2bark/v1/smtp/credentials/"+strconv.FormatInt(credential.ID, 10)+"/test",
		`{"password":"`+rotation.Password+`","subject":"SMTP test","body":"delivery test"}`)
	if response.Code != http.StatusAccepted {
		t.Fatalf("SMTP test failed: status=%d body=%s", response.Code, response.Body.String())
	}
	var testResult struct {
		MessageID int64 `json:"message_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &testResult); err != nil || testResult.MessageID == 0 {
		t.Fatalf("invalid SMTP test response: %s", response.Body.String())
	}
	response = jsonRequest(handler, http.MethodGet, "/mail2bark/v1/messages/"+strconv.FormatInt(testResult.MessageID, 10), "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"delivery test"`) || !strings.Contains(response.Body.String(), `"alert"`) {
		t.Fatalf("message detail failed: status=%d body=%s", response.Code, response.Body.String())
	}

	response = jsonRequest(handler, http.MethodDelete, "/mail2bark/v1/smtp/credentials/"+strconv.FormatInt(credential.ID, 10), "")
	if response.Code != http.StatusConflict {
		t.Fatalf("pending credential deletion status = %d", response.Code)
	}
	store.MarkDelivered(ctx, testResult.MessageID)
	response = jsonRequest(handler, http.MethodDelete, "/mail2bark/v1/smtp/credentials/"+strconv.FormatInt(credential.ID, 10), "")
	if response.Code != http.StatusNoContent {
		t.Fatalf("credential deletion failed: status=%d body=%s", response.Code, response.Body.String())
	}
	response = jsonRequest(handler, http.MethodDelete, "/mail2bark/v1/destinations/"+strconv.FormatInt(destination.ID, 10), "")
	if response.Code != http.StatusNoContent {
		t.Fatalf("destination deletion failed: status=%d body=%s", response.Code, response.Body.String())
	}
}
