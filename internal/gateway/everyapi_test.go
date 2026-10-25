package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

func TestEveryAPIResponsesStreamOnlyUpstream(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if !body.Stream {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"Stream must be set to true","type":"bad_response_status_code"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"tools\":[{\"type\":\"web_search\"}],\"usage\":{\"input_tokens\":2,\"output_tokens\":1}}}\n\n")
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "everyapi-stream", Preset: "everyapi", Name: "EveryAPI", Responses: up.URL + "/v1", Key: "test", Models: []string{"test-model"}}); err != nil {
		t.Fatal(err)
	}
	srv := New()
	for _, streaming := range []bool{false, true} {
		body, _ := json.Marshal(map[string]any{"model": "everyapi-stream/test-model", "input": "Reply OK", "stream": streaming})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(string(body))))
		if rec.Code != 200 {
			t.Fatalf("stream=%v: %d %s", streaming, rec.Code, rec.Body)
		}
		if streaming {
			if !strings.Contains(rec.Header().Get("Content-Type"), "text/event-stream") || !strings.Contains(rec.Body.String(), "response.completed") {
				t.Fatal("streaming client did not receive SSE")
			}
		} else {
			var reply struct {
				Status string            `json:"status"`
				Tools  []json.RawMessage `json:"tools"`
				Output []json.RawMessage `json:"output"`
			}
			if json.Unmarshal(rec.Body.Bytes(), &reply) != nil || reply.Status != "completed" || len(reply.Tools) != 1 || len(reply.Output) != 1 || !strings.Contains(rec.Body.String(), "OK") {
				t.Fatalf("non-streaming client lost response fields or text: %s", rec.Body)
			}
		}
	}
}

func TestEveryAPIPreservesUnspecifiedThinking(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if _, added := body["thinking"]; added {
			t.Error("EveryAPI request gained an unsolicited thinking parameter")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"m1","type":"message","role":"assistant","model":"kimi-k2.7-code","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`)
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "everyapi-thinking", Preset: "everyapi", Name: "EveryAPI", Anthropic: up.URL, Key: "test", Models: []string{"kimi-k2.7-code"}}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"everyapi-thinking/kimi-k2.7-code","max_tokens":128,"messages":[{"role":"user","content":"Reply OK"}]}`)))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestEveryAPIQuotaFailureAcrossClientProtocols(t *testing.T) {
	for _, tc := range []struct{ name, path, body string }{
		{"chat", "/v1/chat/completions", `{"model":"%s/bad","messages":[{"role":"user","content":"OK"}]}`},
		{"responses", "/v1/responses", `{"model":"%s/bad","input":"OK"}`},
		{"anthropic", "/v1/messages", `{"model":"%s/bad","max_tokens":128,"messages":[{"role":"user","content":"OK"}]}`},
		{"gemini", "/v1beta/models/%s/bad:generateContent", `{"contents":[{"role":"user","parts":[{"text":"OK"}]}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				if strings.Contains(string(body), `"bad"`) || strings.Contains(r.URL.Path, "/bad:") {
					w.WriteHeader(429)
					json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "You have exceeded the monthly usage quota. It will reset at " + time.Now().Add(24*time.Hour).Format("2006-01-02 15:04:05 -0700 MST"), "type": "TooManyRequests", "code": "AccountQuotaExceeded", "everyapi_code": "retry_exhausted"}})
					return
				}
				io.WriteString(w, `{"id":"ok","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`)
			}))
			t.Cleanup(up.Close)
			id := "everyapi-quota-wire-" + tc.name
			if err := provider.Save(provider.Provider{ID: id, Preset: "everyapi", Name: "EveryAPI", Chat: up.URL + "/v1", Responses: up.URL + "/v1", Anthropic: up.URL, Gemini: up.URL + "/v1beta", Key: "test", Models: []string{"bad", "good"}}); err != nil {
				t.Fatal(err)
			}
			srv := New()
			path, body := tc.path, tc.body
			if strings.Contains(path, "%s") {
				path = strings.ReplaceAll(path, "%s", id)
			}
			body = strings.ReplaceAll(body, "%s", id)
			for range 3 {
				bad := httptest.NewRecorder()
				srv.Handler().ServeHTTP(bad, httptest.NewRequest("POST", path, strings.NewReader(body)))
				if bad.Code != 429 {
					t.Fatalf("failed model: %d %s", bad.Code, bad.Body)
				}
			}
			good := httptest.NewRecorder()
			srv.Handler().ServeHTTP(good, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"`+id+`/good","messages":[{"role":"user","content":"OK"}]}`)))
			if good.Code != 200 {
				t.Fatalf("unrelated model stopped after %s quota error: %d %s", tc.name, good.Code, good.Body)
			}
		})
	}
}

func TestEveryAPIChannelQuotaDoesNotRestOtherModels(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, tc := range []struct {
		name, preset, code string
		modelOnly          bool
	}{
		{"channel quota", "everyapi", "retry_exhausted", true},
		{"account quota", "everyapi", "insufficient_user_quota", false},
		{"unmarked quota", "everyapi", "", false},
		{"other provider", "", "retry_exhausted", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := candidate{p: provider.Provider{ID: "everyapi-quota-" + tc.name, Preset: tc.preset, Key: "test"}, rest: "everyapi-quota-" + tc.name, model: "doubao-seed-2.0-mini"}
			body, _ := json.Marshal(map[string]any{"error": map[string]any{"message": "You have exceeded the monthly usage quota.", "type": "TooManyRequests", "code": "AccountQuotaExceeded", "everyapi_code": tc.code}})
			r := New().restAfter(c, 429, http.Header{}, body)
			want := c.restKey()
			if tc.modelOnly {
				want = c.restID()
			}
			t.Cleanup(func() {
				restingUntil.Lock()
				delete(restingUntil.m, r.Key)
				delete(restingUntil.note, r.Key)
				restingUntil.Unlock()
			})
			if r.Key != want {
				t.Fatalf("rest key=%q, want %q; a failed upstream channel must not pause unrelated models", r.Key, want)
			}
		})
	}
}
