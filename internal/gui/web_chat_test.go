package gui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebChatPageIsMobileEveryAPIEntry(t *testing.T) {
	was := WebName
	t.Cleanup(func() { WebName = was })
	WebName = "EveryAPI Gateway"
	w := httptest.NewRecorder()
	webChatPage(w, httptest.NewRequest(http.MethodGet, "/chat", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("chat page status: %d", w.Code)
	}
	for _, want := range []string{"和 EveryAPI 聊聊", "/api/chat/models", "/api/chat/completions", "/settings", "class=\"composer-model\"", "/pwa/app.js", "viewport-fit=cover"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("chat page missing %q", want)
		}
	}
	if strings.Contains(w.Body.String(), "/settings?view=sessions") {
		t.Fatal("homepage must not expose the sessions entry")
	}
}

func TestWebChatGatewayUnavailableDoesNotExposeAnything(t *testing.T) {
	was := served.Load()
	t.Cleanup(func() { served.Store(was) })
	served.Store(nil)
	w := httptest.NewRecorder()
	serveWebChatGateway(w, httptest.NewRequest(http.MethodGet, "/api/chat/models", nil), "/v1/models")
	if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "sk-") {
		t.Fatalf("unexpected unavailable response: %d %q", w.Code, w.Body.String())
	}
}
