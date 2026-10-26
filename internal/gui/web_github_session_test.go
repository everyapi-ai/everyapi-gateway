package gui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGitHubConnectionSurvivesRestart(t *testing.T) {
	t.Setenv("EVERYAPI_DATA_DIR", t.TempDir())
	t.Setenv("EVERYAPI_GITHUB_OAUTH_CLIENT_ID", "test-client")
	t.Setenv("EVERYAPI_GITHUB_OAUTH_CLIENT_SECRET", "test-secret")
	t.Setenv("EVERYAPI_GITHUB_ORG", "test-org")
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	http.DefaultTransport = githubTestTransport(func(r *http.Request) (*http.Response, error) {
		body := ""
		switch r.URL.Path {
		case "/login/oauth/access_token":
			body = `{"access_token":"private-test-token"}`
		case "/user":
			body = `{"login":"tester"}`
		case "/orgs/test-org/members/tester":
			body = `{}`
		case "/user/repos":
			if r.Header.Get("Authorization") != "Bearer private-test-token" {
				t.Fatal("lost authorization")
			}
			body = `[{"full_name":"test-org/project","permissions":{"push":true}}]`
		default:
			t.Fatalf("unexpected request: %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	r := httptest.NewRequest("GET", "https://gateway.example/_github/callback?state=state&code=code", nil)
	r.AddCookie(&http.Cookie{Name: githubStateCookie, Value: "state"})
	w := httptest.NewRecorder()
	webGitHubCallback(w, r, loadWebGitHubConfig(), "admin", "admin-key", webCookieAge)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("callback status = %d", w.Code)
	}
	var cookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == githubSessionCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("missing connection cookie")
	}
	t.Cleanup(func() { githubSessions.Delete(cookie.Value) })
	if cookie.MaxAge < int((30*24*time.Hour)/time.Second) {
		t.Errorf("connection cookie expires too soon: %d", cookie.MaxAge)
	}
	githubSessions.Delete(cookie.Value) // Empty process state, retaining the private data directory.
	r = httptest.NewRequest("GET", "/api/github/repos", nil)
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	githubReposAPI(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "test-org/project") {
		t.Fatalf("connection lost after restart: status=%d body=%s", w.Code, w.Body.String())
	}
	s, _ := loadGitHubSession(cookie.Value)
	root := filepath.Join(githubWorkspaceRoot(), "retained")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	s.Workspace = &githubWorkspace{Repo: "test-org/project", Ref: "main", Path: root, Branch: "everyapi/retained"}
	s.Invalid = true
	if err := saveGitHubSessionLocked(s); err != nil {
		t.Fatal(err)
	}
	r = httptest.NewRequest("GET", "https://gateway.example/_github/callback?state=state&code=new-code", nil)
	r.AddCookie(&http.Cookie{Name: githubStateCookie, Value: "state"})
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	webGitHubCallback(w, r, loadWebGitHubConfig(), "admin", "admin-key", webCookieAge)
	if w.Code != http.StatusSeeOther {
		t.Fatal("reconnection failed")
	}
	reconnected, ok := githubSessionFor(r)
	if !ok || reconnected.Workspace == nil || reconnected.Workspace.Branch != "everyapi/retained" || reconnected != s {
		t.Fatal("reconnection lost the current project")
	}
}

func TestGitHubRefreshRetriesPersistenceBeforeFurtherRotation(t *testing.T) {
	t.Setenv("EVERYAPI_DATA_DIR", t.TempDir())
	s := persistedGitHubTestSession(t)
	s.TokenExpires, s.RefreshExpires = time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	refreshes := 0
	http.DefaultTransport = githubTestTransport(func(r *http.Request) (*http.Response, error) {
		refreshes++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":28800,"refresh_token_expires_in":15897600}`))}, nil
	})
	dir := filepath.Dir(githubSessionPath(s.id))
	if err := os.Rename(dir, dir+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("fixture write failure"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := refreshGitHubSessionLocked(context.Background(), s); err == nil {
		t.Fatal("rotation hid a disk error")
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dir+".saved", dir); err != nil {
		t.Fatal(err)
	}
	if err := refreshGitHubSessionLocked(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if refreshes != 1 {
		t.Fatal("disk retry repeated remote rotation")
	}
	restored, err := loadGitHubSession(s.id)
	if err != nil || restored.Token != "new-access" || restored.RefreshToken != "new-refresh" {
		t.Fatal("disk retry did not persist rotated grant")
	}
}

func persistedGitHubTestSession(t *testing.T) *githubSession {
	t.Helper()
	id, err := randomWebGitHubState()
	if err != nil {
		t.Fatal(err)
	}
	s := &githubSession{id: id, binding: githubSessionBinding(), Token: "private-access-token", RefreshToken: "private-refresh-token", Username: "tester", Expires: time.Now().Add(400 * 24 * time.Hour)}
	if err := saveGitHubSessionLocked(s); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { githubSessions.Delete(id) })
	return s
}

func TestGitHubSessionStorageAndDisconnect(t *testing.T) {
	t.Setenv("EVERYAPI_DATA_DIR", t.TempDir())
	s := persistedGitHubTestSession(t)
	path := githubSessionPath(s.id)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte(s.Token)) || bytes.Contains(b, []byte(s.RefreshToken)) || bytes.Contains(b, []byte(s.Username)) {
		t.Fatal("session file contains plaintext credentials")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("session file must be private")
	}
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil || dir.Mode().Perm() != 0o700 {
		t.Fatal("session directory must be private")
	}
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			got, err := loadGitHubSession(s.id)
			if err != nil || got.Token != s.Token {
				t.Error("concurrent restore failed")
			}
		})
	}
	wg.Wait()
	first, _ := loadGitHubSession(s.id)
	second, _ := loadGitHubSession(s.id)
	if first != second {
		t.Fatal("restore created independent writers for one session")
	}
	r := httptest.NewRequest("POST", "https://gateway.example/api/github/disconnect", nil)
	r.AddCookie(&http.Cookie{Name: githubSessionCookie, Value: s.id})
	w := httptest.NewRecorder()
	githubDisconnectAPI(w, r)
	if w.Code != 200 {
		t.Fatalf("disconnect status %d", w.Code)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("disconnect retained grant on disk")
	}
	if _, ok := githubSessionFor(r); ok {
		t.Fatal("disconnected cookie still works")
	}
	if len(w.Result().Cookies()) != 1 || w.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("disconnect did not clear browser cookie")
	}
}

func TestGitHubSessionRejectsCorruptionAndConfigurationChanges(t *testing.T) {
	t.Setenv("EVERYAPI_DATA_DIR", t.TempDir())
	t.Setenv("EVERYAPI_GITHUB_OAUTH_CLIENT_ID", "first-client")
	s := persistedGitHubTestSession(t)
	t.Setenv("EVERYAPI_GITHUB_OAUTH_CLIENT_ID", "different-client")
	if _, err := loadGitHubSession(s.id); err == nil {
		t.Fatal("restored authorization for another OAuth app")
	}
	t.Setenv("EVERYAPI_GITHUB_OAUTH_CLIENT_ID", "first-client")
	path := githubSessionPath(s.id)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b[len(b)-1] ^= 1
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadGitHubSession(s.id); err == nil {
		t.Fatal("accepted corrupt session")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(b, after) {
		t.Fatal("restore overwrote corrupt session")
	}
	if _, err := loadGitHubSession("../../outside"); err == nil {
		t.Fatal("accepted arbitrary cookie path")
	}
}

func TestGitHubSessionRestoresWorkspaceAndInterruptedTask(t *testing.T) {
	t.Setenv("EVERYAPI_DATA_DIR", t.TempDir())
	s := persistedGitHubTestSession(t)
	root := filepath.Join(githubWorkspaceRoot(), "project")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	s.Workspace = &githubWorkspace{Repo: "test-org/project", Ref: "main", Path: root, Branch: "everyapi/fix", LastPrompt: "Fix the issue", PullRequest: map[string]any{"html_url": "https://github.com/test-org/project/pull/1"}, History: []any{map[string]any{"role": "user", "content": "previous task"}}}
	s.Running = true
	if err := saveGitHubSessionLocked(s); err != nil {
		t.Fatal(err)
	}
	restored, err := loadGitHubSession(s.id)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Running || restored.AgentError == "" || restored.Workspace.LastResult == nil || restored.Workspace.Branch != "everyapi/fix" || len(restored.Workspace.History) != 1 || restored.Workspace.PullRequest["html_url"] == nil {
		t.Fatal("restart lost workspace state or left task stuck running")
	}
	s.Expires = time.Now().Add(-time.Hour)
	if err := saveGitHubSessionLocked(s); err != nil {
		t.Fatal(err)
	}
	githubSessions.Delete(s.id)
	if _, err := loadGitHubSession(s.id); err == nil {
		t.Fatal("accepted expired connection")
	}
}

func TestGitHubTokenRefreshIsDurableAndOutageKeepsConnection(t *testing.T) {
	t.Setenv("EVERYAPI_DATA_DIR", t.TempDir())
	s := persistedGitHubTestSession(t)
	s.TokenExpires = time.Now().Add(-time.Hour)
	s.RefreshExpires = time.Now().Add(time.Hour)
	if err := saveGitHubSessionLocked(s); err != nil {
		t.Fatal(err)
	}
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	refreshes := 0
	fail := true
	http.DefaultTransport = githubTestTransport(func(r *http.Request) (*http.Response, error) {
		if fail {
			return nil, errors.New("fixture outage")
		}
		if r.URL.Path == "/login/oauth/access_token" {
			refreshes++
			if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != s.RefreshToken {
				t.Fatal("invalid refresh request")
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"access_token":"rotated-token","refresh_token":"rotated-refresh","expires_in":28800,"refresh_token_expires_in":15897600}`))}, nil
		}
		if r.Header.Get("Authorization") != "Bearer rotated-token" {
			t.Error("API did not use renewed token")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`[]`))}, nil
	})
	r := httptest.NewRequest("GET", "/api/github/repos", nil)
	r.AddCookie(&http.Cookie{Name: githubSessionCookie, Value: s.id})
	w := httptest.NewRecorder()
	githubReposAPI(w, r)
	if w.Code != 502 {
		t.Fatalf("outage = %d, want 502", w.Code)
	}
	if _, ok := githubSessionFor(r); !ok {
		t.Fatal("outage discarded connection")
	}
	fail = false
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			w := httptest.NewRecorder()
			githubReposAPI(w, r)
			if w.Code != 200 {
				t.Errorf("refresh request = %d", w.Code)
			}
		})
	}
	wg.Wait()
	if refreshes != 1 {
		t.Fatalf("rotated tokens %d times", refreshes)
	}
	githubSessions.Delete(s.id)
	restored, err := loadGitHubSession(s.id)
	if err != nil || restored.Token != "rotated-token" || restored.RefreshToken != "rotated-refresh" {
		t.Fatal("restart lost rotated credentials")
	}
	http.DefaultTransport = githubTestTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader(`{"message":"Bad credentials"}`))}, nil
	})
	w = httptest.NewRecorder()
	githubReposAPI(w, r)
	if w.Code != 401 {
		t.Fatalf("revoked token = %d, want reconnect", w.Code)
	}
	if _, ok := githubSessionFor(r); ok {
		t.Fatal("revoked grant still accepted")
	}
}

func TestGitHubSessionAcrossProcesses(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef0123456789abcdef"
	if mode := os.Getenv("MAGPIE_GITHUB_SESSION_PROCESS"); mode != "" {
		if mode == "save" {
			s := &githubSession{id: id, binding: githubSessionBinding(), Token: "process-fixture-token", Username: "tester", Expires: time.Now().Add(400 * 24 * time.Hour)}
			if err := saveGitHubSessionLocked(s); err != nil {
				t.Fatal(err)
			}
		} else {
			if _, exists := githubSessions.Load(id); exists {
				t.Fatal("child inherited in-memory session")
			}
			r := httptest.NewRequest("GET", "/api/github/workspace", nil)
			r.AddCookie(&http.Cookie{Name: githubSessionCookie, Value: id})
			w := httptest.NewRecorder()
			githubWorkspaceAPI(w, r)
			var out map[string]any
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
				t.Fatalf("new process lost connection: %d", w.Code)
			}
		}
		return
	}
	t.Setenv("EVERYAPI_DATA_DIR", t.TempDir())
	for _, mode := range []string{"save", "restore"} {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestGitHubSessionAcrossProcesses$", "-test.count=1")
		cmd.Env = append(os.Environ(), "MAGPIE_GITHUB_SESSION_PROCESS="+mode)
		b, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("%s process: %v\n%s", mode, err, b)
		}
	}
}

func TestGitHubRateLimitsDoNotRequireReconnection(t *testing.T) {
	t.Setenv("EVERYAPI_DATA_DIR", t.TempDir())
	s := persistedGitHubTestSession(t)
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	for _, status := range []int{403, 429, 500} {
		http.DefaultTransport = githubTestTransport(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"message":"temporary failure"}`))}, nil
		})
		r := httptest.NewRequest("GET", "/api/github/repos", nil)
		r.AddCookie(&http.Cookie{Name: githubSessionCookie, Value: s.id})
		w := httptest.NewRecorder()
		githubReposAPI(w, r)
		if w.Code != 502 {
			t.Fatalf("GitHub HTTP %d resulted in gateway HTTP %d", status, w.Code)
		}
		if _, ok := githubSessionFor(r); !ok {
			t.Fatalf("GitHub HTTP %d discarded connection", status)
		}
	}
}
