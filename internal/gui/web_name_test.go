package gui

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
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
	if w.Code != 401 || !strings.Contains(w.Body.String(), "EveryAPI Gateway") || strings.Contains(w.Body.String(), "EVERYAPI_WEB_KEY") || strings.Contains(w.Body.String(), "admin-key") {
		t.Fatal("distribution's sign-in page uses the wrong product or key")
	}
	WebName = "<script>bad</script>"
	page = string(versionedPage([]byte("<title>magpie</title>")))
	if strings.Contains(page, "<script>") {
		t.Fatal("product name injected HTML")
	}
}

func TestWebDistributionSignInLanguage(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	was := WebName
	t.Cleanup(func() { WebName = was })
	WebName = "EveryAPI Gateway"
	const key = "private-administrator-key-123456"
	h := webGuard("session", key, webCookieAge, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for _, tc := range []struct{ pref, accept, lang, button, missing, wrong, stale string }{
		{"zh", "en-US", "zh", "登录", "请输入管理密码", "管理密码不正确", "登录已失效"},
		{"en", "zh-CN", "en", "Sign in", "Enter your management password", "Incorrect management password", "Your sign-in has expired"},
		{"system", "zh-TW,zh;q=0.9", "zh-TW", "登入", "請輸入管理密碼", "管理密碼不正確", "登入已失效"},
		{"system", "ja-JP,en;q=0.8", "ja", "ログイン", "管理パスワードを入力", "管理パスワードが正しくありません", "ログインの有効期限が切れました"},
		{"de", "en", "de", "Anmelden", "Verwaltungspasswort eingeben", "Falsches Verwaltungspasswort", "Ihre Anmeldung ist abgelaufen"},
	} {
		t.Run(tc.lang, func(t *testing.T) {
			if err := settings.Save(settings.Settings{Lang: tc.pref}); err != nil {
				t.Fatal(err)
			}
			for _, kind := range []string{"missing", "wrong", "stale", "invalid-link"} {
				r := httptest.NewRequest("GET", "/?view=usage", nil)
				want := tc.missing
				if kind == "wrong" {
					r = httptest.NewRequest("POST", webSignInPath, strings.NewReader(url.Values{"key": {"wrong"}, "back": {"/?view=usage"}}.Encode()))
					r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					want = tc.wrong
				} else if kind == "stale" {
					r.AddCookie(&http.Cookie{Name: "session", Value: "old"})
					want = tc.stale
				} else if kind == "invalid-link" {
					r = httptest.NewRequest("GET", "/?view=usage&k=wrong-private-key", nil)
					want = tc.wrong
				}
				r.Header.Set("Accept", "text/html")
				r.Header.Set("Accept-Language", tc.accept)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				body := w.Body.String()
				if w.Code != 401 || !strings.Contains(body, `lang="`+tc.lang+`"`) || !strings.Contains(body, want) || !strings.Contains(body, ">"+tc.button+"</button>") || !strings.Contains(body, `value="/?view=usage"`) {
					t.Fatalf("%s sign-in page was not localized: status=%d body=%s", kind, w.Code, body)
				}
				if strings.Contains(body, key) || strings.Contains(body, "wrong-private-key") || strings.Contains(body, "per host, not per port") || len(w.Result().Cookies()) != 0 {
					t.Fatal("sign-in page exposed a credential, internal diagnosis, or authenticated an invalid key")
				}
			}
		})
	}
}
