package gui

import (
	"encoding/json"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebDistributionPWA(t *testing.T) {
	was, pageWas := WebName, webPage.Load()
	t.Cleanup(func() { WebName = was; webPage.Store(pageWas) })
	WebName = "EveryAPI Gateway"
	webPage.Store(true)
	h := webHandler("8080", "administrator-secret", webCookieAge, http.NotFoundHandler())
	for _, path := range []string{"/pwa/manifest.webmanifest", "/pwa/sw.js", "/pwa/offline", "/pwa/icon-192.png", "/pwa/icon-512.png", "/pwa/maskable-512.png", "/pwa/apple-touch-icon.png"} {
		r := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 || len(w.Result().Cookies()) != 0 || strings.Contains(w.Body.String(), "administrator-secret") {
			t.Fatalf("public installation resource %s: status %d, must work without a session and contain no credentials", path, w.Code)
		}
		if path == "/pwa/manifest.webmanifest" {
			var m struct {
				ID, Name, StartURL, Scope, Display string
				Icons                              []struct{ Src, Sizes, Purpose string }
			}
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
				t.Fatal(err)
			}
			json.Unmarshal(raw["start_url"], &m.StartURL)
			if m.ID != "/" || m.StartURL != "/" || m.Scope != "/" || m.Name != WebName || m.Display != "standalone" || len(m.Icons) < 3 {
				t.Fatalf("unusable installation manifest: %s", w.Body)
			}
		}
		if path == "/pwa/sw.js" && (w.Header().Get("Service-Worker-Allowed") != "/" || w.Header().Get("Cache-Control") != "no-store") {
			t.Fatal("worker must control the start URL and revalidate on updates")
		}
		if strings.HasSuffix(path, ".png") {
			c, err := png.DecodeConfig(strings.NewReader(w.Body.String()))
			if err != nil || c.Width != c.Height || c.Width < 180 {
				t.Fatalf("invalid home-screen icon %s: %+v %v", path, c, err)
			}
		}
	}
	for _, path := range []string{"/", "/api/settings", "/boot.js", "/pwa/app.js", "/pwa/../api/settings"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("PWA bypassed authentication for %s: %d", path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/pwa/sw.js", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("public resources accepted a write: %d", w.Code)
	}
	page := string(versionedPage([]byte("<head></head><body></body>")))
	if !strings.Contains(page, `rel="manifest"`) || !strings.Contains(page, `apple-touch-icon`) || !strings.Contains(page, `pwa/app.js?v=`) {
		t.Fatal("authenticated distribution page does not advertise PWA installation")
	}
	WebName = ""
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/pwa/manifest.webmanifest", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatal("standalone PWA leaked into ordinary Magpie")
	}
}
