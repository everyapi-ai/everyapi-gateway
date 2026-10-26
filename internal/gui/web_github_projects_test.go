package gui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

func TestGitDiffIncludesUntrackedFiles(t *testing.T) {
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("new content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	diff := gitDiff(context.Background(), root)
	if !strings.Contains(diff, "new content") || !strings.Contains(diff, "new.txt") {
		t.Fatalf("untracked file missing from diff: %q", diff)
	}
}

func githubTestRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.name", "Test"}, {"config", "user.email", "test@example.com"}} {
		if err := gitRun(context.Background(), root, args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "read me.txt"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := gitRun(context.Background(), root, "add", "."); err != nil {
		t.Fatal(err)
	}
	if err := gitRun(context.Background(), root, "commit", "-qm", "Initial"); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestGitHubWorkspaceFilePreview(t *testing.T) {
	root := githubTestRepo(t)
	id := t.Name()
	githubSessions.Store(id, &githubSession{Expires: time.Now().Add(time.Hour), Workspace: &githubWorkspace{Path: root}})
	t.Cleanup(func() { githubSessions.Delete(id) })
	for name, content := range map[string][]byte{".env": []byte("private"), "binary.dat": {0, 1, 2}, "large.txt": []byte(strings.Repeat("中", 100000))} {
		if err := os.WriteFile(filepath.Join(root, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		path string
		auth bool
		code int
	}{
		{"read%20me.txt", false, http.StatusUnauthorized},
		{"read%20me.txt", true, http.StatusOK},
		{"large.txt", true, http.StatusOK},
		{"../outside", true, http.StatusBadRequest},
		{".env", true, http.StatusBadRequest},
		{".git/config", true, http.StatusBadRequest},
		{"binary.dat", true, http.StatusUnsupportedMediaType},
		{"missing.txt", true, http.StatusNotFound},
	} {
		r := httptest.NewRequest(http.MethodGet, "/api/github/file?path="+tc.path, nil)
		if tc.auth {
			r.AddCookie(&http.Cookie{Name: githubSessionCookie, Value: id})
		}
		w := httptest.NewRecorder()
		githubWorkspaceFileAPI(w, r)
		if w.Code != tc.code {
			t.Fatalf("%s authenticated=%v: %d %s", tc.path, tc.auth, w.Code, w.Body.String())
		}
		if tc.code == http.StatusOK {
			var out struct {
				Content   string `json:"content"`
				Truncated bool   `json:"truncated"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if tc.path == "large.txt" {
				if !out.Truncated || len(out.Content) > 256<<10 || strings.ContainsRune(out.Content, '\uFFFD') {
					t.Fatal("preview did not bound large UTF-8 text safely")
				}
			} else if out.Content != "before\n" {
				t.Fatalf("preview content = %q", out.Content)
			}
		}
	}
}

func TestGitHubChangedFilesPreservesPaths(t *testing.T) {
	root := githubTestRepo(t)
	if err := os.WriteFile(filepath.Join(root, "read me.txt"), []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := changedFiles(context.Background(), root); !reflect.DeepEqual(got, []string{"read me.txt"}) {
		t.Fatalf("changed files = %q", got)
	}
	if err := gitRun(context.Background(), root, "add", "."); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gitDiff(context.Background(), root), "+after") {
		t.Fatal("staged changes disappeared from the review diff")
	}
}

func TestGitHubDirectoryToolsBoundLargeRepositories(t *testing.T) {
	root := githubTestRepo(t)
	if err := os.Mkdir(filepath.Join(root, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	for i := range 260 {
		if err := os.WriteFile(filepath.Join(root, "src", fmt.Sprintf("file-%03d.txt", i)), []byte("example"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	call := func(name, arguments string) string {
		return githubToolResult(root, githubToolCall{Name: name, Arguments: json.RawMessage(arguments)})
	}
	first := call("list_files", `{"path":"."}`)
	if strings.Contains(first, "file-259") || !strings.Contains(first, "src/") {
		t.Fatalf("root listing recursively dumped the project: %d bytes", len(first))
	}
	page := call("list_files", `{"path":"src"}`)
	if strings.Contains(page, "file-259") || !strings.Contains(page, "next_offset") {
		t.Fatalf("large directory was not paginated: %d bytes", len(page))
	}
	last := call("list_files", `{"path":"src","offset":200}`)
	if !strings.Contains(last, "file-259") || strings.Contains(last, "file-000") {
		t.Fatal("directory pagination lost or repeated entries")
	}
	search := call("search_files", `{"query":"file-259"}`)
	if !strings.Contains(search, "src/file-259.txt") || strings.Contains(search, "file-000") {
		t.Fatal("filename search could not locate a nested project file")
	}
	search = call("search_files", `{"query":".txt"}`)
	if !strings.Contains(search, "next_offset") || strings.Contains(search, "file-259") {
		t.Fatal("filename search dumped a large result without pagination")
	}
	text := strings.Repeat("a", 20<<10) + "END"
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	chunk := call("read_file", `{"path":"large.txt"}`)
	if len(chunk) > 17<<10 || !strings.Contains(chunk, "next_offset") {
		t.Fatalf("large read did not bound model input: %d bytes", len(chunk))
	}
	end := call("read_file", `{"path":"large.txt","offset":20480}`)
	if end != "END" {
		t.Fatalf("file offset did not resume reading: %d bytes", len(end))
	}
	unicode := strings.Repeat("文件", 4000)
	if err := os.WriteFile(filepath.Join(root, "unicode.txt"), []byte(unicode), 0o600); err != nil {
		t.Fatal(err)
	}
	var part struct {
		Content string `json:"content"`
		Offset  int    `json:"next_offset"`
	}
	if err := json.Unmarshal([]byte(call("read_file", `{"path":"unicode.txt"}`)), &part); err != nil {
		t.Fatal(err)
	}
	rest := call("read_file", fmt.Sprintf(`{"path":"unicode.txt","offset":%d}`, part.Offset))
	if part.Content+rest != unicode {
		t.Fatal("paged reads split or lost a UTF-8 character")
	}
	if got := call("edit_file", `{"path":"large.txt","old_text":"END","new_text":"DONE"}`); got != "edited" {
		t.Fatalf("focused edit failed: %s", got)
	}
	if got := call("edit_file", `{"path":"large.txt","old_text":"aa","new_text":"bad"}`); !strings.Contains(got, "exactly once") {
		t.Fatal("ambiguous edit was not rejected")
	}
	updated, err := os.ReadFile(filepath.Join(root, "large.txt"))
	if err != nil || string(updated) != strings.TrimSuffix(text, "END")+"DONE" {
		t.Fatal("focused edit changed unrelated file content")
	}
}

func TestGitHubToolsRejectMetadataAndSymlinks(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{filepath.Join(root, ".git", "config"), filepath.Join(outside, "private.txt")} {
		if err := os.WriteFile(file, []byte("private fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(".git", filepath.Join(root, "metadata")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../private.txt", ".git/config", "escape/private.txt", "escape/new.txt", "metadata/config"} {
		for _, tool := range []string{"read_file", "write_file", "edit_file"} {
			args, _ := json.Marshal(map[string]string{"path": name, "content": "overwritten", "old_text": "private fixture", "new_text": "overwritten"})
			got := githubToolResult(root, githubToolCall{Name: tool, Arguments: args})
			if got == "written" || got == "edited" || strings.Contains(got, "private fixture") {
				t.Errorf("%s accessed %q: %s", tool, name, got)
			}
		}
	}
	files, err := workspaceFiles(root, 6)
	if err != nil || len(files) != 0 {
		t.Errorf("file listing exposed protected paths: %q, %v", files, err)
	}
	args := json.RawMessage(`{"path":"src/new.txt","content":"safe"}`)
	if got := githubToolResult(root, githubToolCall{Name: "write_file", Arguments: args}); got != "written" {
		t.Fatalf("normal nested write failed: %s", got)
	}
}

type githubTestTransport func(*http.Request) (*http.Response, error)

func (f githubTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGitHubPullRequestRetriesCommittedChanges(t *testing.T) {
	root := githubTestRepo(t)
	remote := t.TempDir()
	if err := gitRun(context.Background(), remote, "init", "--bare", "-q"); err != nil {
		t.Fatal(err)
	}
	if err := gitRun(context.Background(), root, "remote", "add", "origin", remote); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "read me.txt"), []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &githubSession{Token: "fixture", Username: "tester", Expires: time.Now().Add(time.Hour), Workspace: &githubWorkspace{Repo: "everyapi-ai/test", Ref: "main", Path: root}}
	id := t.Name()
	githubSessions.Store(id, s)
	t.Cleanup(func() { githubSessions.Delete(id) })
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	calls := 0
	http.DefaultTransport = githubTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.github.com" || r.URL.Path != "/repos/everyapi-ai/test/pulls" {
			t.Fatalf("unexpected network request: %s %s", r.Method, r.URL)
		}
		if r.Method == http.MethodGet {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("[]")), Header: make(http.Header)}, nil
		}
		calls++
		code, body := 201, `{"html_url":"https://github.com/everyapi-ai/test/pull/1","number":1}`
		if calls == 1 {
			code, body = 502, `{"message":"injected failure after git push"}`
		}
		return &http.Response{StatusCode: code, Status: http.StatusText(code), Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	for i, want := range []int{502, 200, 200} {
		req := httptest.NewRequest(http.MethodPost, "/api/github/pull-request", strings.NewReader(`{"title":"Update documentation","body":"A test change"}`))
		req.AddCookie(&http.Cookie{Name: githubSessionCookie, Value: id})
		rec := httptest.NewRecorder()
		githubPullRequestAPI(rec, req)
		if rec.Code != want {
			t.Fatalf("attempt %d = %d %s, want %d", i, rec.Code, rec.Body.String(), want)
		}
	}
	if calls != 2 {
		t.Fatalf("repeated request created another PR: %d calls", calls)
	}
	if n, err := gitRunOutput(context.Background(), root, "rev-list", "--count", "HEAD"); err != nil || strings.TrimSpace(n) != "2" {
		t.Fatalf("retry made duplicate commits: %q, %v", n, err)
	}
}

func TestGitHubAgentResumesAfterBrowserDisconnect(t *testing.T) {
	gitPath := os.Getenv("PATH")
	sandboxHome(t)
	t.Setenv("PATH", gitPath)
	root := githubTestRepo(t)
	if err := os.WriteFile(filepath.Join(root, "context.txt"), []byte(strings.Repeat("example line\n", 2000)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := gitRun(context.Background(), root, "add", "context.txt"); err != nil {
		t.Fatal(err)
	}
	if err := gitRun(context.Background(), root, "commit", "-qm", "Add context fixture"); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var released sync.Once
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		raw, err := io.ReadAll(r.Body)
		if len(raw) > 96<<10 {
			t.Error("model input exceeded the bounded project context")
		}
		if err != nil {
			t.Error(err)
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Error(err)
		}
		n := calls.Add(1)
		if n == 1 {
			close(started)
			select {
			case <-release:
			case <-r.Context().Done():
				t.Error("the task inherited the browser request cancellation")
				return
			}
		}
		if n > 1 {
			found := false
			for _, message := range body.Messages {
				if toolCalls, ok := message["tool_calls"].([]any); ok {
					for _, item := range toolCalls {
						if item.(map[string]any)["type"] != "function" {
							t.Error("assistant tool call lost its type on the next model request")
						}
					}
					found = true
				}
			}
			if !found {
				t.Error("model did not receive the previous tool calls")
			}
		}
		w.Header().Set("Content-Type", "application/json")
		message := map[string]any{"role": "assistant", "content": "Updated the file."}
		if n <= 12 {
			name, arguments := "read_file", `{"path":"context.txt"}`
			if n == 12 {
				name, arguments = "write_file", `{"path":"read me.txt","content":"after\n"}`
			}
			message = map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": fmt.Sprintf("call-%d", n), "type": "function", "function": map[string]any{"name": name, "arguments": arguments}}}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message, "finish_reason": "stop"}}})
	}))
	t.Cleanup(up.Close)
	t.Cleanup(func() { released.Do(func() { close(release) }) })
	if err := provider.Save(provider.Provider{ID: "everyapi", Name: "EveryAPI", Chat: up.URL, Key: "fixture", Models: []string{"test"}}); err != nil {
		t.Fatal(err)
	}
	old := served.Swap(gateway.New())
	t.Cleanup(func() { served.Store(old) })
	id := t.Name()
	s := &githubSession{Token: "fixture", Expires: time.Now().Add(time.Hour), Workspace: &githubWorkspace{Repo: "everyapi-ai/test", Ref: "main", Path: root}}
	githubSessions.Store(id, s)
	t.Cleanup(func() { githubSessions.Delete(id) })
	request := func(method, path, body string) *http.Request {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: githubSessionCookie, Value: id})
		return r
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	w := httptest.NewRecorder()
	githubAgentAPI(w, request(http.MethodPost, "/api/github/agent", `{"model":"everyapi/test","prompt":"Update the file."}`).WithContext(ctx))
	if w.Code != http.StatusAccepted {
		t.Fatalf("task was not accepted asynchronously: %d %s", w.Code, w.Body.String())
	}
	cancel()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the model request never started")
	}
	busy := httptest.NewRecorder()
	githubSelectWorkspaceAPI(busy, request(http.MethodPost, "/api/github/workspace", `{"repo":"everyapi-ai/other"}`))
	if busy.Code != http.StatusConflict {
		t.Fatalf("switching projects during a task = %d", busy.Code)
	}
	released.Do(func() { close(release) })
	deadline := time.After(5 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		status := httptest.NewRecorder()
		githubAgentStatusAPI(status, request(http.MethodGet, "/api/github/agent", ""))
		var job struct {
			Running  bool           `json:"running"`
			Error    string         `json:"error"`
			Result   map[string]any `json:"result"`
			Activity []string       `json:"activity"`
		}
		if err := json.Unmarshal(status.Body.Bytes(), &job); err != nil {
			t.Fatal(err)
		}
		if !job.Running {
			if !strings.Contains(strings.Join(job.Activity, "\n"), "write_file read me.txt") {
				t.Fatal("task activity did not expose the real file edit")
			}
			if job.Error != "" || job.Result == nil || !strings.Contains(fmt.Sprint(job.Result["diff"]), "+after") {
				t.Fatalf("resumed task did not produce its diff: %s", status.Body.String())
			}
			break
		}
		select {
		case <-tick.C:
		case <-deadline:
			t.Fatal("task did not finish")
		}
	}
	current := httptest.NewRecorder()
	githubWorkspaceAPI(current, request(http.MethodGet, "/api/github/workspace", ""))
	if !strings.Contains(current.Body.String(), "Update the file.") || !strings.Contains(current.Body.String(), "Updated the file.") {
		t.Fatalf("refresh lost the prompt or result: %s", current.Body.String())
	}
}

func TestGitHubCancelAgentRequiresSessionAndPreservesWorkspace(t *testing.T) {
	root := githubTestRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	id := t.Name()
	s := &githubSession{Expires: time.Now().Add(time.Hour), Running: true, Cancel: cancel, Workspace: &githubWorkspace{Path: root}}
	githubSessions.Store(id, s)
	t.Cleanup(func() { githubSessions.Delete(id) })
	for _, authenticated := range []bool{false, true, true} {
		r := httptest.NewRequest(http.MethodDelete, "/api/github/agent", nil)
		if authenticated {
			r.AddCookie(&http.Cookie{Name: githubSessionCookie, Value: id})
		}
		w := httptest.NewRecorder()
		githubCancelAgentAPI(w, r)
		if !authenticated {
			if w.Code != http.StatusUnauthorized || ctx.Err() != nil {
				t.Fatal("unauthorized caller stopped a task")
			}
		} else if w.Code != http.StatusOK || ctx.Err() != context.Canceled || !s.Running {
			t.Fatalf("stop must cancel without exposing the workspace before task cleanup: %d", w.Code)
		}
	}
	if b, err := os.ReadFile(filepath.Join(root, "read me.txt")); err != nil || string(b) != "before\n" {
		t.Fatal("stop lost the workspace contents")
	}
}

func TestGitHubReposPaginatesWritableProjects(t *testing.T) {
	id := t.Name()
	githubSessions.Store(id, &githubSession{Token: "fixture", Username: "tester", Expires: time.Now().Add(time.Hour)})
	t.Cleanup(func() { githubSessions.Delete(id) })
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	pages := 0
	http.DefaultTransport = githubTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.github.com" || r.URL.Path != "/user/repos" {
			t.Fatalf("unexpected repository request: %s", r.URL)
		}
		pages++
		var repos []map[string]any
		if r.URL.Query().Get("page") == "1" {
			for i := range 100 {
				repos = append(repos, map[string]any{"full_name": fmt.Sprintf("yldm-tech/repo%d", i), "permissions": map[string]bool{"push": i != 99}, "archived": i == 98})
			}
		} else {
			repos = []map[string]any{{"full_name": "everyapi-ai/last", "permissions": map[string]bool{"push": true}}}
		}
		b, _ := json.Marshal(repos)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(b)), Header: make(http.Header)}, nil
	})
	r := httptest.NewRequest(http.MethodGet, "/api/github/repos", nil)
	r.AddCookie(&http.Cookie{Name: githubSessionCookie, Value: id})
	w := httptest.NewRecorder()
	githubReposAPI(w, r)
	var out struct {
		Repos []struct {
			FullName string `json:"full_name"`
		} `json:"repos"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || len(out.Repos) != 99 || pages != 2 {
		t.Fatalf("paginated projects: status=%d count=%d pages=%d error=%v", w.Code, len(out.Repos), pages, err)
	}
	if out.Repos[0].FullName != "yldm-tech/repo0" || out.Repos[len(out.Repos)-1].FullName != "everyapi-ai/last" {
		t.Fatal("accessible repositories across organizations were lost")
	}
}

func TestStoredGitHubOAuthConfigIsPrivateAndReloadable(t *testing.T) {
	t.Setenv("EVERYAPI_DATA_DIR", t.TempDir())
	for _, name := range []string{
		"EVERYAPI_GITHUB_OAUTH_CLIENT_ID", "EVERYAPI_GITHUB_CLIENT_ID",
		"EVERYAPI_GITHUB_OAUTH_CLIENT_SECRET", "EVERYAPI_GITHUB_CLIENT_SECRET",
		"EVERYAPI_GITHUB_OAUTH_CLIENT_SECRET_FILE", "EVERYAPI_GITHUB_CLIENT_SECRET_FILE",
		"EVERYAPI_GITHUB_ORG",
	} {
		t.Setenv(name, "")
	}
	want := githubOAuthStoredConfig{ClientID: "Iv1.test-client", ClientSecret: "secret-value-123", Organization: "everyapi-ai"}
	if err := saveStoredGitHubConfig(want); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(githubOAuthConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Mode().Perm(); got != 0o600 {
		t.Fatalf("OAuth config mode = %o, want 600", got)
	}
	if got := loadWebGitHubConfig(); got.ClientID != want.ClientID || got.ClientSecret != want.ClientSecret || got.Organization != want.Organization || !got.enabled() {
		t.Fatalf("stored OAuth config did not reload: %+v", got)
	}
}

func TestGitHubOAuthConfigAPIDoesNotReturnSecret(t *testing.T) {
	was := WebEveryAPIOnly
	t.Cleanup(func() { WebEveryAPIOnly = was })
	WebEveryAPIOnly = true
	t.Setenv("EVERYAPI_DATA_DIR", t.TempDir())
	mux := http.NewServeMux()
	githubProjectRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/github/config", strings.NewReader(`{"client_id":"Iv1.test","client_secret":"secret-value-123","organization":"everyapi-ai"}`)))
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "secret-value-123") {
		t.Fatalf("config response leaked or failed: %d %s", rec.Code, rec.Body.String())
	}
	var status map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil || status["configured"] != true {
		t.Fatalf("unexpected config response: %s", rec.Body.String())
	}
}
