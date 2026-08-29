package mail2bark

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
