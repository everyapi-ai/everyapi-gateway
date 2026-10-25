package gui

import (
	"encoding/json"
	"html/template"
	"net/http"
)

// Installation resources are public so launchers can fetch icons and metadata without the browser's sign-in cookie. No console or API response is served here.
func webPWA(w http.ResponseWriter, r *http.Request) bool {
	if WebName == "" {
		return false
	}
	switch r.URL.Path {
	case "/pwa/manifest.webmanifest", "/pwa/offline", "/pwa/sw.js", "/pwa/icon-192.png", "/pwa/icon-512.png", "/pwa/maskable-512.png", "/pwa/apple-touch-icon.png":
	default:
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return true
	}
	switch r.URL.Path {
	case "/pwa/manifest.webmanifest":
		w.Header().Set("Content-Type", "application/manifest+json; charset=utf-8")
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(map[string]any{
				"id": "/", "name": WebName, "short_name": "EveryAPI", "start_url": "/", "scope": "/", "display": "standalone",
				"background_color": "#f5f6f8", "theme_color": "#202124", "prefer_related_applications": false,
				"icons": []map[string]string{
					{"src": "/pwa/icon-192.png", "sizes": "192x192", "type": "image/png", "purpose": "any"},
					{"src": "/pwa/icon-512.png", "sizes": "512x512", "type": "image/png", "purpose": "any"},
					{"src": "/pwa/maskable-512.png", "sizes": "512x512", "type": "image/png", "purpose": "maskable"},
				},
			})
		}
	case "/pwa/offline":
		lang := webSignInLang(r)
		words := webOfflineTranslations[lang]
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Language", lang)
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.Method == http.MethodGet {
			_ = webOfflinePage.Execute(w, struct{ Lang, Name, Title, Message, Retry string }{lang, WebName, words[0], words[1], words[2]})
		}
	default:
		if r.URL.Path == "/pwa/sw.js" {
			w.Header().Set("Service-Worker-Allowed", "/")
		}
		http.FileServer(http.FS(staticFS())).ServeHTTP(w, r)
	}
	return true
}

const webPWAHead = `<link rel="manifest" href="/pwa/manifest.webmanifest"><link rel="apple-touch-icon" href="/pwa/apple-touch-icon.png"><meta name="theme-color" content="#202124"><meta name="mobile-web-app-capable" content="yes"><meta name="apple-mobile-web-app-capable" content="yes"><meta name="apple-mobile-web-app-title" content="EveryAPI"><meta name="apple-mobile-web-app-status-bar-style" content="default">`

var webOfflineTranslations = map[string][3]string{
	"en":    {"Cannot reach the gateway", "Check your connection and make sure the gateway is running, then try again.", "Reconnect"},
	"zh":    {"暂时无法连接网关", "请检查网络，并确认网关正在运行，然后重试。", "重新连接"},
	"zh-TW": {"暫時無法連線至閘道", "請檢查網路，並確認閘道正在執行，然後重試。", "重新連線"},
	"ja":    {"ゲートウェイに接続できません", "ネットワーク接続とゲートウェイの稼働状況を確認して、もう一度お試しください。", "再接続"},
	"de":    {"Gateway nicht erreichbar", "Prüfen Sie Ihre Verbindung und ob das Gateway läuft. Versuchen Sie es danach erneut.", "Erneut verbinden"},
}

// Only this credential-free document is stored offline; its reconnect link never retains a sign-in URL's query parameters.
var webOfflinePage = template.Must(template.New("offline").Parse(`<!doctype html><html lang="{{.Lang}}"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover"><meta name="color-scheme" content="light dark"><title>{{.Name}}</title><style>html,body{background:#f5f6f8;color:#202124}body{margin:0;font:16px/1.6 system-ui,sans-serif;padding: max(32px,env(safe-area-inset-top)) max(20px,env(safe-area-inset-right)) max(32px,env(safe-area-inset-bottom)) max(20px,env(safe-area-inset-left))}main{max-width:420px;margin:12vh auto}h1{font-size:24px}a{display:inline-block;background:#202124;color:#fff;padding:12px 24px;border-radius:10px;text-decoration:none}@media(prefers-color-scheme:dark){html,body{background:#18191c;color:#eee}a{background:#eee;color:#202124}}</style></head><body><main><p>{{.Name}}</p><h1>{{.Title}}</h1><p>{{.Message}}</p><a href="/">{{.Retry}}</a></main></body></html>`))
