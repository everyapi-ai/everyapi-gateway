package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/agentenv"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/pluginrt"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/testenv"
)

func TestMain(m *testing.M) {
	testenv.Offline()
	testenv.Main(m)
}

func isolate(t *testing.T) {
	t.Helper()
	for _, name := range append(append([]string{}, agentenv.Vars...), "HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "APPDATA", "LOCALAPPDATA", "MAGPIE_ADDR", "MAGPIE_PUBLIC_URL", "MAGPIE_WEB_KEY", "MAGPIE_NO_ACCOUNTS", "MAGPIE_NO_STATS") {
		t.Setenv(name, os.Getenv(name))
	}
	for _, name := range []string{"EVERYAPI_PUBLIC_URL", "EVERYAPI_INTERNAL_ADDR", "EVERYAPI_API_BASE", "EVERYAPI_REGION", "EVERYAPI_API_KEY", "EVERYAPI_API_KEY_FILE", "EVERYAPI_WEB_KEY", "EVERYAPI_WEB_KEY_FILE", "EVERYAPI_GATEWAY_KEY", "EVERYAPI_GATEWAY_KEY_FILE"} {
		t.Setenv(name, "")
	}
}

func TestAPIRoot(t *testing.T) {
	for _, c := range []struct{ base, region, want string }{
		{"", "global", "https://api.everyapi.ai"},
		{"", "cn", "https://api-cn.everyapi.ai"},
		{"https://api.everyapi.ai/v1/", "global", "https://api.everyapi.ai"},
		{"http://127.0.0.1:8787/prefix/v1", "global", "http://127.0.0.1:8787/prefix"},
		{"https://host.test/base", "cn", "https://host.test/base"},
	} {
		got, err := apiRoot(c.base, c.region)
		if err != nil || got != c.want {
			t.Errorf("%s %s: %q %v", c.base, c.region, got, err)
		}
	}
	for _, base := range []string{"ftp://host.test", "https://user:password@host.test", "https://host.test?key=secret", "https://host.test#fragment", "https://host.test/a/../b", "https://host.test/%2e%2e", "https://", "/v1"} {
		if _, err := apiRoot(base, "global"); err == nil {
			t.Errorf("accepted invalid base %q", base)
		}
	}
	if _, err := apiRoot("", "typo"); err == nil {
		t.Fatal("accepted invalid region")
	}
}

func TestHealthcheckRequestsSignInPage(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/html" {
			http.Error(w, "no sign-in cookie", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("<form>Sign in</form>"))
	}))
	defer s.Close()
	u, _ := url.Parse(s.URL)
	if err := healthcheck(u.Host); err != nil {
		t.Fatal(err)
	}
}

func TestPreparePreservesEditsAndRevocation(t *testing.T) {
	isolate(t)
	data := t.TempDir()
	t.Setenv("EVERYAPI_REGION", "cn")
	t.Setenv("EVERYAPI_API_KEY", "test-upstream-key")
	t.Setenv("CODEX_HOME", filepath.Join(t.TempDir(), "real-agent"))
	if err := prepare(data, "127.0.0.1:8080"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(settings.Dir(), data+string(filepath.Separator)) || os.Getenv("CODEX_HOME") != "" || !settings.AccountDiscoveryOff() {
		t.Fatal("server did not isolate the operator's agents")
	}
	if os.Getenv("MAGPIE_PUBLIC_URL") != "http://127.0.0.1:8080" {
		t.Fatal("client connection URL points to the internal listener")
	}
	p, err := provider.Find("everyapi")
	if err != nil || p.Chat != "https://api-cn.everyapi.ai/v1" || p.Gemini != "https://api-cn.everyapi.ai/v1beta" || p.Key != "test-upstream-key" {
		t.Fatal("EveryAPI China endpoints or credentials were not provisioned")
	}
	admin, err := os.ReadFile(filepath.Join(data, "admin-key"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"admin-key", "client-key"} {
		fi, err := os.Stat(filepath.Join(data, name))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s is not private", name)
		}
	}
	p.Key, p.Chat = "edited-in-web", "https://custom.test/v1"
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	keys, err := access.List()
	if err != nil || len(keys) != 1 {
		t.Fatalf("bootstrap client key: %v %v", keys, err)
	}
	if _, err := access.Update("remove-key", access.Change{Key: keys[0].ID}); err != nil {
		t.Fatal(err)
	}
	if err := prepare(data, "127.0.0.1:8080"); err != nil {
		t.Fatal(err)
	}
	p, _ = provider.Find("everyapi")
	if p.Key != "edited-in-web" || p.Chat != "https://custom.test/v1" || os.Getenv("MAGPIE_WEB_KEY") != strings.TrimSpace(string(admin)) {
		t.Fatal("restart replaced a Web edit or changed the administrator key")
	}
	if keys, err := access.List(); err != nil || len(keys) != 0 {
		t.Fatal("restart resurrected a revoked key")
	}
	if err := os.WriteFile(provider.Path(), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(provider.Path() + ".bak"); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := prepare(data, "127.0.0.1:8080"); err == nil {
		t.Fatal("corrupt provider store was treated as a new installation")
	}
}

func TestPrepareWithoutUpstreamKey(t *testing.T) {
	for _, region := range []string{"global", "cn"} {
		t.Run(region, func(t *testing.T) {
			isolate(t)
			t.Setenv("EVERYAPI_REGION", region)
			data := t.TempDir()
			if err := prepare(data, "127.0.0.1:8080"); err != nil {
				t.Fatalf("first start without an upstream key must allow Web setup: %v", err)
			}
			if _, err := os.Stat(provider.Path()); !os.IsNotExist(err) {
				t.Fatal("blank-key startup must leave provider setup to the Web UI")
			}
			if keys, err := access.List(); err != nil || len(keys) != 1 {
				t.Fatalf("Web setup did not provision a client key: %v", err)
			}
			if err := prepare(data, "127.0.0.1:8080"); err != nil {
				t.Fatalf("restart before Web setup failed: %v", err)
			}
		})
	}
}

func TestSecretFilesAndInvalidConfiguration(t *testing.T) {
	isolate(t)
	file := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(file, []byte("file-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EVERYAPI_API_KEY_FILE", file)
	if got, err := secret("EVERYAPI_API_KEY"); err != nil || got != "file-secret" {
		t.Fatalf("secret file: %q %v", got, err)
	}
	t.Setenv("EVERYAPI_API_KEY", "environment-secret")
	if _, err := secret("EVERYAPI_API_KEY"); err == nil {
		t.Fatal("ambiguous credential sources accepted")
	}
	t.Setenv("EVERYAPI_API_KEY_FILE", "")
	for _, c := range []struct{ name, value string }{
		{"EVERYAPI_WEB_KEY", "short"},
		{"EVERYAPI_GATEWAY_KEY", "environment-secret"},
		{"EVERYAPI_GATEWAY_KEY", "with whitespace invalid"},
	} {
		t.Run(c.name+c.value, func(t *testing.T) {
			t.Setenv(c.name, c.value)
			data := filepath.Join(t.TempDir(), "uncreated")
			if err := prepare(data, "127.0.0.1:8080"); err == nil {
				t.Fatal("invalid credentials accepted")
			}
			if _, err := os.Stat(data); !os.IsNotExist(err) {
				t.Fatal("invalid configuration changed persistent state")
			}
		})
	}
}

func TestEveryAPIMaskingRelay(t *testing.T) {
	isolate(t)
	const sensitive = "private-project-acceptance"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Messages) != 1 {
			t.Error("upstream received an invalid request")
			w.WriteHeader(400)
			return
		}
		text := body.Messages[0].Content
		if strings.Contains(text, sensitive) || !strings.Contains(text, "{{") {
			t.Error("upstream received an unmasked value")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "masked", "object": "chat.completion", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": text}, "finish_reason": "stop"}}})
	}))
	defer upstream.Close()
	t.Setenv("EVERYAPI_API_BASE", upstream.URL)
	t.Setenv("EVERYAPI_API_KEY", "test-upstream-key")
	if err := prepare(t.TempDir(), "127.0.0.1:8080"); err != nil {
		t.Fatal(err)
	}
	st := settings.Load()
	st.RedactWords = []string{sensitive}
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	rt := pluginrt.New(pluginrt.Registered())
	defer rt.Stop()
	rt.Reconcile(map[string]bool{"redact": true}, nil)
	p, _ := provider.Find("everyapi")
	p.Models = []string{"demo-model"}
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"everyapi/demo-model","messages":[{"role":"user","content":"`+sensitive+`"}]}`))
	r.RemoteAddr = "127.0.0.1:50000"
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	gateway.New().Front().ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), sensitive) {
		t.Fatalf("masking relay did not restore the reply: %d %s", w.Code, w.Body.String())
	}
}

func TestEveryAPIAuthenticatedRelay(t *testing.T) {
	isolate(t)
	was := gateway.RequireClientKey
	gateway.RequireClientKey = true
	t.Cleanup(func() { gateway.RequireClientKey = was })
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer upstream-only-key" && r.Header.Get("X-Api-Key") != "upstream-only-key" && r.Header.Get("X-Goog-Api-Key") != "upstream-only-key" {
			t.Error("upstream received a client's key instead of its own")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/models" {
			ioWrite(w, `{"object":"list","data":[{"id":"demo-model","object":"model"}]}`)
			return
		}
		if r.URL.Path == "/v1beta/models" {
			ioWrite(w, `{"models":[{"name":"models/demo-model","supportedGenerationMethods":["generateContent"]}]}`)
			return
		}
		if r.URL.Path == "/v1beta/models/demo-model:generateContent" {
			ioWrite(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1,"totalTokenCount":3}}`)
			return
		}
		var body struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != "demo-model" {
			t.Error("upstream did not receive the unprefixed model id")
			w.WriteHeader(400)
			return
		}
		switch r.URL.Path {
		case "/v1/chat/completions":
			if body.Stream {
				w.Header().Set("Content-Type", "text/event-stream")
				ioWrite(w, "data: {\"id\":\"chat-demo\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"chat-demo\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			} else {
				ioWrite(w, `{"id":"chat-demo","object":"chat.completion","model":"demo-model","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
			}
		case "/v1/responses":
			ioWrite(w, `{"id":"resp-demo","object":"response","status":"completed","model":"demo-model","output":[{"id":"msg-demo","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"hello","annotations":[]}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
		case "/v1/messages":
			ioWrite(w, `{"id":"msg-demo","type":"message","role":"assistant","model":"demo-model","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	t.Setenv("EVERYAPI_API_BASE", upstream.URL+"/v1")
	t.Setenv("EVERYAPI_API_KEY", "upstream-only-key")
	t.Setenv("EVERYAPI_WEB_KEY", "administrator-only-key")
	t.Setenv("EVERYAPI_GATEWAY_KEY", "client-only-key-12345")
	if err := prepare(t.TempDir(), "127.0.0.1:8080"); err != nil {
		t.Fatal(err)
	}
	p, _ := provider.Find("everyapi")
	if models, err := p.Fetch(context.Background()); err != nil || len(models) == 0 {
		t.Fatalf("discover models: %v", err)
	}
	h := gateway.New().Front()
	request := func(peer, key, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.RemoteAddr = peer
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, peer := range []string{"203.0.113.42:50000", "127.0.0.1:50000"} {
		for _, key := range []string{"", "wrong-key", "upstream-only-key", "administrator-only-key"} {
			if w := request(peer, key, "GET", "/v1/models", ""); w.Code == 200 {
				t.Fatalf("gateway accepted an unissued client key from %s", peer)
			}
		}
	}
	for _, c := range []struct{ path, body, contains string }{
		{"/v1/models", "", "everyapi/demo-model"},
		{"/v1/chat/completions", `{"model":"everyapi/demo-model","messages":[{"role":"user","content":"hi"}]}`, "hello"},
		{"/v1/chat/completions", `{"model":"everyapi/demo-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`, "[DONE]"},
		{"/v1/responses", `{"model":"everyapi/demo-model","input":"hi"}`, "hello"},
		{"/v1/messages", `{"model":"everyapi/demo-model","max_tokens":32,"messages":[{"role":"user","content":"hi"}]}`, "hello"},
		{"/v1beta/models/everyapi/demo-model:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, "hello"},
	} {
		method := "POST"
		if c.body == "" {
			method = "GET"
		}
		w := request("203.0.113.42:50000", "client-only-key-12345", method, c.path, c.body)
		if w.Code != 200 || !strings.Contains(w.Body.String(), c.contains) {
			t.Fatalf("%s: %d %s", c.path, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "upstream-only-key") {
			t.Fatal("upstream key leaked to client")
		}
	}
}

func ioWrite(w http.ResponseWriter, body string) { _, _ = w.Write([]byte(body)) }
