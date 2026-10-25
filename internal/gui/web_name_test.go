package gui

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebDistributionName(t *testing.T) {
	was, pageWas := WebName, webPage.Load()
	t.Cleanup(func() { WebName = was; webPage.Store(pageWas) })
	WebName = "EveryAPI Gateway"
	webPage.Store(true)
	page := string(versionedPage([]byte("<title>magpie</title><div class=brand><span>magpie</span></div>")))
	if !strings.Contains(page, "<title>EveryAPI Gateway</title>") || !strings.Contains(page, "<span>EveryAPI Gateway</span>") {
		t.Fatal("distribution name is missing from browser UI")
	}
	w := httptest.NewRecorder()
	webTurnedAway(w, httptest.NewRequest("GET", "/", nil), "/", "Sign in")
	if w.Code != 401 || !strings.Contains(w.Body.String(), "EveryAPI Gateway") || !strings.Contains(w.Body.String(), "EVERYAPI_WEB_KEY") {
		t.Fatal("distribution's sign-in page uses the wrong product or key")
	}
	WebName = "<script>bad</script>"
	page = string(versionedPage([]byte("<title>magpie</title>")))
	if strings.Contains(page, "<script>") {
		t.Fatal("product name injected HTML")
	}
}
