package gui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestEveryAPIOnlyConfiguration(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	was, root := WebEveryAPIOnly, WebEveryAPIBase
	t.Cleanup(func() { WebEveryAPIOnly, WebEveryAPIBase = was, root })
	WebEveryAPIOnly, WebEveryAPIBase = true, "https://api.everyapi.ai"
	mux := http.NewServeMux()
	everyAPIRoutes(mux)
	call := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/everyapi", strings.NewReader(body)))
		return w
	}
	const key = "everyapi-private-test-key-123456"
	if w := call(`{"key":"` + key + `","region":"global"}`); w.Code != 200 || strings.Contains(w.Body.String(), key) {
		t.Fatalf("save: status=%d; credential must remain private", w.Code)
	}
	p, err := provider.Find("everyapi")
	if err != nil || p.Key != key || p.Chat != "https://api.everyapi.ai/v1" {
		t.Fatal("EveryAPI configuration was not saved")
	}
	if w := call(`{"key":"","region":"cn"}`); w.Code != 200 {
		t.Fatalf("region save: %d", w.Code)
	}
	p, err = provider.Find("everyapi")
	if err != nil || p.Key != key || p.Chat != "https://api-cn.everyapi.ai/v1" {
		t.Fatal("region edit lost the saved key")
	}
	if w := call(`{"region":"https://other.invalid"}`); w.Code != 400 {
		t.Fatalf("arbitrary upstream: %d", w.Code)
	}
}

func TestEveryAPIOnlyRejectsGenericManagement(t *testing.T) {
	was := WebEveryAPIOnly
	t.Cleanup(func() { WebEveryAPIOnly = was })
	h := everyAPIOnly(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	for _, enabled := range []bool{true, false} {
		WebEveryAPIOnly = enabled
		for _, path := range []string{"/api/provider/save", "/api/provider/remove", "/api/plugins/add", "/api/provider/import", "/api/providers/arrange"} {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(`{"id":"other"}`)))
			want := 204
			if enabled {
				want = 404
			}
			if w.Code != want {
				t.Errorf("only=%v %s: %d", enabled, path, w.Code)
			}
		}
	}
}
