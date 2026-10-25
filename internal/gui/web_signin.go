package gui

import (
	"html/template"
	"net/http"

	"golang.org/x/text/language"

	"github.com/yetone/magpie/internal/settings"
)

type webLoginWords struct {
	Title, Label, Button, Hint, Missing, Stale, Wrong string
}

// The sign-in page is rendered before authenticated JavaScript can load.
var webLoginTranslations = map[string]webLoginWords{
	"en":    {"Management sign-in", "Management password", "Sign in", "Use the management password configured for this gateway. This is separate from your EveryAPI API key.", "Enter your management password to open the gateway console.", "Your sign-in has expired. Enter your management password to continue.", "Incorrect management password. Please try again."},
	"zh":    {"登录管理控制台", "管理密码", "登录", "使用此网关的管理密码登录。EveryAPI API 密钥在登录后配置。", "请输入管理密码，进入网关管理控制台。", "登录已失效，请重新输入管理密码。", "管理密码不正确，请重新输入。"},
	"zh-TW": {"登入管理控制台", "管理密碼", "登入", "使用此閘道的管理密碼登入。EveryAPI API 金鑰在登入後設定。", "請輸入管理密碼，進入閘道管理控制台。", "登入已失效，請重新輸入管理密碼。", "管理密碼不正確，請重新輸入。"},
	"ja":    {"管理コンソールにログイン", "管理パスワード", "ログイン", "このゲートウェイの管理パスワードでログインしてください。EveryAPI API キーはログイン後に設定します。", "管理パスワードを入力して、ゲートウェイの管理コンソールを開いてください。", "ログインの有効期限が切れました。管理パスワードを再入力してください。", "管理パスワードが正しくありません。もう一度入力してください。"},
	"de":    {"Verwaltungsanmeldung", "Verwaltungspasswort", "Anmelden", "Mit dem Verwaltungspasswort dieses Gateways anmelden. Der EveryAPI-API-Schlüssel wird nach der Anmeldung eingerichtet.", "Verwaltungspasswort eingeben, um die Gateway-Verwaltung zu öffnen.", "Ihre Anmeldung ist abgelaufen. Geben Sie Ihr Verwaltungspasswort erneut ein.", "Falsches Verwaltungspasswort. Bitte versuchen Sie es erneut."},
}

var webLoginPage = template.Must(template.New("sign-in").Parse(`<!doctype html><html lang="{{.Lang}}"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">{{.PWA}}<meta name="color-scheme" content="light dark"><title>{{.Name}}</title>
<style>html,body{background:#f5f6f8;color:#202124}body{font:15px/1.6 system-ui,sans-serif;margin:0;padding:max(24px,env(safe-area-inset-top)) max(16px,env(safe-area-inset-right)) max(24px,env(safe-area-inset-bottom)) max(16px,env(safe-area-inset-left));box-sizing:border-box;min-height:100vh;min-height:100dvh;display:flex;align-items:center}main{box-sizing:border-box;width:100%;max-width:420px;margin:auto;padding:clamp(20px,6vw,32px);background:#fff;border:1px solid #e1e4e8;border-radius:16px}h1{font-size:22px;margin:4px 0 16px}.brand{font-size:13px;color:#666}label{display:block;font-weight:600;margin-top:24px}input,button{font:inherit;font-size:16px;min-height:44px;box-sizing:border-box;width:100%;border-radius:8px;padding:10px 12px}input{border:1px solid #aeb4bc;background:#fff;color:#202124;margin:8px 0 16px}button{border:0;background:#202124;color:#fff;cursor:pointer}.hint{font-size:13px;color:#666;margin-bottom:0}@media(prefers-color-scheme:dark){html,body{background:#18191c;color:#eee}main{background:#222428;border-color:#3d4048}.brand,.hint{color:#b4bac4}input{background:#18191c;color:#eee;border-color:#6a707a}button{background:#eee;color:#202124}}</style></head>
<body><main><div class="brand">{{.Name}}</div><h1>{{.Words.Title}}</h1><p role="status">{{.Message}}</p><form method="post" action="{{.Action}}"><input type="hidden" name="back" value="{{.Back}}"><label for="management-password">{{.Words.Label}}</label><input id="management-password" type="password" name="key" autocomplete="current-password" autofocus required><button type="submit">{{.Words.Button}}</button></form><p class="hint">{{.Words.Hint}}</p></main></body></html>`))

func webSignInLang(r *http.Request) string {
	return trayLang(settings.Load().Lang, func() string {
		tags, _, _ := language.ParseAcceptLanguage(r.Header.Get("Accept-Language"))
		if len(tags) > 0 {
			return tags[0].String()
		}
		return "en"
	})
}

func webDistributionSignIn(w http.ResponseWriter, r *http.Request, back, why string) {
	lang := webSignInLang(r)
	words := webLoginTranslations[lang]
	message := words.Missing
	switch why {
	case webStaleCookie:
		message = words.Stale
	case webWrongKey:
		message = words.Wrong
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Language", lang)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(http.StatusUnauthorized)
	_ = webLoginPage.Execute(w, struct {
		Lang, Name, Message, Action, Back string
		Words                             webLoginWords
		PWA                               template.HTML
	}{lang, WebName, message, webSignInPath, localPath(back), words, template.HTML(webPWAHead)})
}
