package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xJogger/fake-komga-115/internal/database"
)

func TestStaleServiceWorker(t *testing.T) {
	store, err := database.Open(filepath.Join(t.TempDir(), "service-worker.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	handler := New(store, nil, nil, nil, nil, nil, nil, nil, RuntimeInfo{}, nil).Handler()

	request := func(method, path, serviceWorker string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		if serviceWorker != "" {
			req.Header.Set("Service-Worker", serviceWorker)
		}
		req.Header.Set("Accept-Encoding", "gzip")
		resp := httptest.NewRecorder()
		resp.Header().Add("Vary", "Origin")
		handler.ServeHTTP(resp, req)
		return resp
	}

	for _, state := range []string{"unclaimed", "claimed"} {
		if state == "claimed" {
			claimAndLoginCookie(t, store)
		}
		t.Run(state, func(t *testing.T) {
			for _, tc := range []struct{ method, path, serviceWorker string }{
				{http.MethodGet, "/sw.js", "script"},
				{http.MethodGet, "/assets/sw.js", " ScRiPt "},
				{http.MethodHead, "/sw.js", "script"},
			} {
				t.Run(tc.method+" "+tc.path, func(t *testing.T) {
					resp := request(tc.method, tc.path, tc.serviceWorker)
					if resp.Code != http.StatusOK {
						t.Fatalf("status = %d, want 200", resp.Code)
					}
					for key, want := range map[string]string{
						"Content-Type":           "application/javascript; charset=utf-8",
						"Cache-Control":          "no-store",
						"Service-Worker-Allowed": "/",
						"Content-Encoding":       "",
						"X-Content-Type-Options": "nosniff",
					} {
						if got := resp.Header().Get(key); got != want {
							t.Errorf("%s = %q, want %q", key, got, want)
						}
					}
					if vary := resp.Header().Values("Vary"); !slices.Equal(vary, []string{"Origin", "Service-Worker"}) {
						t.Errorf("Vary = %v, want [Origin Service-Worker]", vary)
					}
					if tc.method == http.MethodHead {
						if resp.Body.Len() != 0 {
							t.Errorf("HEAD body has %d bytes", resp.Body.Len())
						}
						return
					}
					body := resp.Body.String()
					start := 0
					for _, statement := range []string{"self.skipWaiting()", "caches.delete(k)", "await self.registration.unregister()", "self.clients.matchAll({ type: 'window' })", "c.navigate('/')"} {
						index := strings.Index(body[start:], statement)
						if index < 0 {
							t.Fatalf("script is missing %q or runs it out of order", statement)
						}
						start += index + len(statement)
					}
					if strings.Contains(body, "clients.claim(") || strings.Contains(body, "c.url") {
						t.Error("script must release control and navigate to the bookshelf")
					}
				})
			}

			for _, tc := range []struct {
				method, path, serviceWorker string
				status                      int
			}{
				{http.MethodGet, "/sw.js", "", http.StatusTemporaryRedirect},
				{http.MethodGet, "/sw.js", "other", http.StatusTemporaryRedirect},
				{http.MethodPost, "/sw.js", "script", http.StatusTemporaryRedirect},
				{http.MethodGet, "/assets/sw.js", "", http.StatusNotFound},
				{http.MethodGet, "/api", "script", http.StatusUnauthorized},
				{http.MethodGet, "/api/v1/libraries", "script", http.StatusUnauthorized},
				{http.MethodGet, "/admin/api/status", "script", http.StatusUnauthorized},
				{http.MethodGet, "/opds/v1.2/catalog", "script", http.StatusUnauthorized},
			} {
				resp := request(tc.method, tc.path, tc.serviceWorker)
				if resp.Code != tc.status || resp.Header().Get("Service-Worker-Allowed") != "" {
					t.Errorf("%s %s (Service-Worker %q): status = %d, want %d with no Service-Worker-Allowed", tc.method, tc.path, tc.serviceWorker, resp.Code, tc.status)
				}
			}
		})
	}
}

func TestHealthServiceMarker(t *testing.T) {
	handler := New(nil, nil, nil, nil, nil, nil, nil, nil, RuntimeInfo{}, nil).Handler()
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/health", nil))
	var health map[string]string
	err := json.Unmarshal(resp.Body.Bytes(), &health)
	if resp.Code != http.StatusOK || err != nil || len(health) != 2 || health["status"] != "ok" || health["service"] != "mangaalpha" {
		t.Fatalf("health status = %d, body = %q, JSON error = %v", resp.Code, resp.Body.String(), err)
	}
}
