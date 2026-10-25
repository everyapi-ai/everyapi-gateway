package gui

import (
	"html/template"
	"io/fs"
	"net/http"
)

// webChatRoutes is the small, mobile-first surface of the EveryAPI
// distribution. It is deliberately separate from Magpie's desktop console:
// the browser gets a same-origin, cookie-protected chat API and never sees the
// upstream EveryAPI credential.
func webChatRoutes(mux *http.ServeMux) {
	if !WebEveryAPIOnly {
		return
	}
	mux.HandleFunc("GET /api/chat/models", func(w http.ResponseWriter, r *http.Request) {
		serveWebChatGateway(w, r, "/v1/models")
	})
	mux.HandleFunc("POST /api/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		serveWebChatGateway(w, r, "/v1/chat/completions")
	})
}

// serveWebChatGateway forwards a signed-in browser request to the in-process
// gateway without going through the public LAN guard. The web guard has
// already authenticated the administrator cookie; the upstream provider key
// remains entirely inside the gateway process.
func serveWebChatGateway(w http.ResponseWriter, r *http.Request, path string) {
	gw := served.Load()
	if gw == nil {
		http.Error(w, "EveryAPI gateway is not available", http.StatusServiceUnavailable)
		return
	}
	clone := r.Clone(r.Context())
	clone.URL.Path = path
	clone.URL.RawPath = ""
	clone.RequestURI = ""
	gw.Handler().ServeHTTP(w, clone)
}

func webChatPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Language", "zh-CN")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	_ = webChatTemplate.Execute(w, struct {
		Name string
		PWA  template.HTML
	}{WebName, template.HTML(webPWAHead)})
}

// The page is intentionally dependency-free: it works in Safari on an
// iPhone, in an installed PWA, and in a normal desktop browser. The stream
// parser accepts OpenAI-compatible SSE and also handles a non-stream JSON
// response from a proxy or an upstream that ignores stream=true.
var webChatTemplate = template.Must(template.New("everyapi-chat").Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
{{.PWA}}<link rel="stylesheet" href="/pwa/app.css"><script src="/pwa/app.js" defer></script><meta name="color-scheme" content="light dark"><title>{{.Name}} · Chat</title>
<style>
:root{color-scheme:light dark;--bg:#f5f6f8;--panel:#fff;--text:#17181a;--muted:#69707a;--line:#dfe3e8;--accent:#1e5eff;--user:#e8efff;--assistant:#f1f3f5;--danger:#c62828}
*{box-sizing:border-box}html,body{margin:0;background:var(--bg);color:var(--text);font:16px/1.55 system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}body{min-height:100dvh;display:flex;flex-direction:column;padding-top:env(safe-area-inset-top);padding-bottom:env(safe-area-inset-bottom)}
header{height:60px;display:flex;align-items:center;gap:10px;padding:8px max(14px,env(safe-area-inset-right)) 8px max(14px,env(safe-area-inset-left));background:var(--panel);border-bottom:1px solid var(--line);position:sticky;top:0;z-index:2}header h1{font-size:17px;white-space:nowrap;margin:0;font-weight:700}button,a{font:inherit}button{border:0;cursor:pointer;color:var(--text)}.icon{width:42px;height:42px;padding:0;border-radius:10px;background:transparent;font-size:21px}.icon:active,.ghost:active{background:var(--assistant)}
main{width:min(920px,100%);flex:1;display:flex;flex-direction:column;margin:0 auto;min-height:0}.messages{flex:1;overflow:auto;padding:20px max(14px,env(safe-area-inset-right)) 110px max(14px,env(safe-area-inset-left));scroll-behavior:smooth}.empty{max-width:500px;margin:18vh auto 0;text-align:center;color:var(--muted)}.empty h2{color:var(--text);font-size:25px;margin:0 0 8px}.empty p{margin:0}.msg{display:flex;margin:0 auto 18px;max-width:820px}.msg.user{justify-content:flex-end}.bubble{max-width:min(86%,720px);padding:11px 14px;border-radius:16px;white-space:pre-wrap;overflow-wrap:anywhere}.user .bubble{background:var(--user);border-bottom-right-radius:5px}.assistant .bubble{background:var(--assistant);border-bottom-left-radius:5px}.error .bubble{background:#ffebee;color:var(--danger)}.meta{font-size:12px;color:var(--muted);margin:0 4px 5px}.assistant-wrap{max-width:min(86%,720px)}
.composer-wrap{position:fixed;left:0;right:0;bottom:0;padding:10px max(14px,env(safe-area-inset-right)) max(12px,env(safe-area-inset-bottom)) max(14px,env(safe-area-inset-left));background:linear-gradient(transparent,var(--bg) 22%);pointer-events:none}.composer{width:min(820px,100%);margin:0 auto;display:grid;grid-template-columns:1fr;gap:4px;padding:8px;background:var(--panel);border:1px solid var(--line);border-radius:16px;box-shadow:0 5px 22px #0001;pointer-events:auto}.composer-model{width:100%;height:36px;border:1px solid var(--line);border-radius:9px;padding:0 9px;background:var(--panel);color:var(--text);font-size:16px}.composer-row{display:flex;align-items:flex-end;gap:8px}.composer textarea{flex:1;min-width:0;resize:none;max-height:180px;min-height:42px;border:0;outline:0;background:transparent;color:var(--text);font:16px/1.45 system-ui,sans-serif;padding:9px 6px}.send{flex:0 0 auto;width:42px;height:42px;border-radius:11px;background:var(--accent);color:#fff;font-size:19px}.send[disabled]{opacity:.5;cursor:default}.stop{display:none;background:#fff0f0;color:var(--danger)}.busy .send{display:none}.busy .stop{display:block}.status{position:fixed;top:67px;left:50%;transform:translateX(-50%);z-index:3;background:#25282dcc;color:#fff;padding:6px 11px;border-radius:9px;font-size:13px;max-width:calc(100% - 28px);white-space:nowrap;overflow:hidden;text-overflow:ellipsis}.hidden{display:none!important}.settings{color:var(--muted);text-decoration:none;font-size:13px;white-space:nowrap;padding:8px 2px}.settings:active{color:var(--text)}
@media(min-width:700px){header{padding-left:24px;padding-right:24px}.messages{padding-left:24px;padding-right:24px}.composer-wrap{padding-left:24px;padding-right:24px}}
@media(prefers-color-scheme:dark){:root{--bg:#17181b;--panel:#222428;--text:#f2f3f5;--muted:#a8afb9;--line:#3a3e46;--user:#263b68;--assistant:#2a2d33}.error .bubble{background:#442328}.status{background:#f4f4f4;color:#17181a}}
</style></head><body>
<header><h1>EveryAPI</h1><div class="actions" data-pwa-actions></div><a class="settings" href="/settings">设置</a><button class="icon" id="clear" title="清空对话" aria-label="清空对话">⌫</button></header>
<main><section id="messages" class="messages" aria-live="polite"><div id="empty" class="empty"><h2>和 EveryAPI 聊聊</h2><p>选择模型后，在下方输入消息。</p></div></section></main>
<div id="status" class="status hidden" role="status"></div>
<div class="composer-wrap"><form id="composer" class="composer"><select id="model" class="composer-model" aria-label="模型" disabled><option>加载模型…</option></select><div class="composer-row"><textarea id="input" rows="1" maxlength="20000" placeholder="输入消息…" aria-label="消息"></textarea><button id="send" class="send" type="submit" aria-label="发送">➤</button><button id="stop" class="send stop" type="button" aria-label="停止生成">■</button></div></form></div>
<script>
(() => {
  const model = document.querySelector('#model'), messagesEl = document.querySelector('#messages'), empty = document.querySelector('#empty'), input = document.querySelector('#input'), form = document.querySelector('#composer'), clear = document.querySelector('#clear'), status = document.querySelector('#status'), stop = document.querySelector('#stop');
  let messages = [], controller = null, assistantBubble = null;
  const showStatus = (text, error = false) => { status.textContent = text; status.classList.toggle('hidden', !text); status.style.background = error ? '#b3261e' : ''; if (text) setTimeout(() => { if (status.textContent === text) status.classList.add('hidden'); }, 4200); };
  const scroll = () => { messagesEl.scrollTop = messagesEl.scrollHeight; };
  const draw = (role, text, error = false) => { empty.classList.add('hidden'); const row = document.createElement('article'); row.className = 'msg ' + (error ? 'error' : role); const wrap = document.createElement('div'); if (role === 'assistant') wrap.className = 'assistant-wrap'; if (role === 'assistant') { const meta = document.createElement('div'); meta.className = 'meta'; meta.textContent = model.value || 'EveryAPI'; wrap.append(meta); } const bubble = document.createElement('div'); bubble.className = 'bubble'; bubble.textContent = text; wrap.append(bubble); row.append(wrap); messagesEl.append(row); scroll(); return bubble; };
  const setBusy = on => { form.classList.toggle('busy', on); input.disabled = on; model.disabled = on; clear.disabled = on; };
  const readError = async res => { const text = await res.text(); try { const data = JSON.parse(text); return data?.error?.message || data?.error || text || res.statusText; } catch { return text || res.statusText; } };
  async function loadModels() { try { const res = await fetch('/api/chat/models', { headers: {Accept: 'application/json'} }); if (!res.ok) throw new Error(await readError(res)); const data = await res.json(); const list = Array.isArray(data?.data) ? data.data : []; model.replaceChildren(); list.forEach(item => { const option = document.createElement('option'); option.value = item.id; option.textContent = item.id; model.append(option); }); if (!list.length) { const option = document.createElement('option'); option.textContent = '暂无模型，请先到设置配置 EveryAPI'; option.value = ''; model.append(option); showStatus('暂无模型，请先到设置配置 EveryAPI', true); } model.disabled = false; } catch (error) { model.replaceChildren(new Option('模型加载失败', '')); model.disabled = false; showStatus(error.message || '模型加载失败', true); } }
  function addDelta(text) { if (!assistantBubble) assistantBubble = draw('assistant', ''); assistantBubble.textContent += text; scroll(); }
  async function send() { const text = input.value.trim(); if (!text || !model.value || controller) return; input.value = ''; input.style.height = 'auto'; messages.push({role: 'user', content: text}); draw('user', text); controller = new AbortController(); setBusy(true); assistantBubble = null; const history = messages.map(m => ({role: m.role, content: m.content})); try { const res = await fetch('/api/chat/completions', {method:'POST', headers:{'Content-Type':'application/json','Accept':'text/event-stream, application/json'}, body: JSON.stringify({model:model.value, messages:history, stream:true}), signal:controller.signal}); if (!res.ok) throw new Error(await readError(res)); const type = res.headers.get('content-type') || ''; if (type.includes('text/event-stream')) { const reader = res.body.getReader(), decoder = new TextDecoder(); let buffer = ''; while (true) { const part = await reader.read(); if (part.done) break; buffer += decoder.decode(part.value, {stream:true}); const lines = buffer.split(/\\r?\\n/); buffer = lines.pop() || ''; for (const line of lines) { const value = line.startsWith('data:') ? line.slice(5).trim() : ''; if (!value || value === '[DONE]') continue; try { const data = JSON.parse(value); const delta = data?.choices?.[0]?.delta?.content ?? data?.choices?.[0]?.text ?? ''; if (delta) addDelta(delta); } catch {} } } } else { const data = await res.json(); const text = data?.choices?.[0]?.message?.content ?? data?.choices?.[0]?.text ?? ''; addDelta(typeof text === 'string' ? text : JSON.stringify(text)); } if (assistantBubble) messages.push({role:'assistant', content:assistantBubble.textContent}); if (!assistantBubble) addDelta('（模型没有返回文本）'); } catch (error) { if (error.name !== 'AbortError') { draw('assistant', error.message || '请求失败', true); messages.pop(); } } finally { controller = null; setBusy(false); input.focus({preventScroll:true}); } }
  form.addEventListener('submit', event => { event.preventDefault(); send(); }); stop.addEventListener('click', () => controller?.abort()); clear.addEventListener('click', () => { if (controller) return; messages = []; messagesEl.replaceChildren(empty); empty.classList.remove('hidden'); input.focus({preventScroll:true}); }); input.addEventListener('input', () => { input.style.height = 'auto'; input.style.height = Math.min(input.scrollHeight, 180) + 'px'; }); input.addEventListener('keydown', event => { if (event.key === 'Enter' && !event.shiftKey) { event.preventDefault(); send(); } }); loadModels(); input.focus({preventScroll:true});
})();
</script></body></html>`))

// webConsolePage keeps the existing management UI available at /settings
// after the EveryAPI home page becomes the mobile chat surface.
func webConsolePage(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(staticFS(), "index.html")
	if err != nil {
		http.Error(w, "console is unavailable", http.StatusInternalServerError)
		return
	}
	b = versionedPage(b)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}
