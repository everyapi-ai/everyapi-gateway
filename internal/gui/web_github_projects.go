package gui

// GitHub project access for the EveryAPI web gateway. OAuth tokens stay in
// memory only; a restart requires the user to authorize GitHub again. Repos
// are cloned into the gateway's private data directory and all file paths are
// checked before an agent can read or write them.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/settings"
)

type githubSession struct {
	Token, Username string
	Expires         time.Time
	Workspace       *githubWorkspace
}

type githubWorkspace struct {
	Repo, Ref, Path string
}

var githubSessions sync.Map // session id -> *githubSession

var githubRepoName = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func githubSessionFor(r *http.Request) (*githubSession, bool) {
	c, err := r.Cookie(githubSessionCookie)
	if err != nil || c.Value == "" {
		return nil, false
	}
	v, ok := githubSessions.Load(c.Value)
	s, ok := v.(*githubSession)
	if !ok || s == nil || time.Now().After(s.Expires) {
		if ok {
			githubSessions.Delete(c.Value)
		}
		return nil, false
	}
	return s, true
}

func githubProjectRoutes(mux *http.ServeMux) {
	if !WebEveryAPIOnly {
		return
	}
	mux.HandleFunc("GET /api/github/repos", githubReposAPI)
	mux.HandleFunc("GET /api/github/workspace", githubWorkspaceAPI)
	mux.HandleFunc("POST /api/github/workspace", githubSelectWorkspaceAPI)
	mux.HandleFunc("POST /api/github/agent", githubAgentAPI)
	mux.HandleFunc("POST /api/github/pull-request", githubPullRequestAPI)
}

func githubReposAPI(w http.ResponseWriter, r *http.Request) {
	s, ok := githubSessionFor(r)
	if !ok {
		http.Error(w, "请先授权 GitHub", http.StatusUnauthorized)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	var repos []struct {
		FullName      string `json:"full_name"`
		Name          string `json:"name"`
		Private       bool   `json:"private"`
		DefaultBranch string `json:"default_branch"`
		Description   string `json:"description"`
		Updated       string `json:"updated_at"`
	}
	if err := githubJSON(ctx, s.Token, http.MethodGet, "https://api.github.com/user/repos?per_page=100&sort=updated&affiliation=owner%2Ccollaborator%2Corganization_member", nil, &repos); err != nil {
		http.Error(w, "无法读取 GitHub 项目列表", http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{"user": s.Username, "repos": repos})
}

func githubWorkspaceAPI(w http.ResponseWriter, r *http.Request) {
	s, ok := githubSessionFor(r)
	if !ok {
		http.Error(w, "请先授权 GitHub", http.StatusUnauthorized)
		return
	}
	if s.Workspace == nil {
		writeJSON(w, map[string]any{"selected": false})
		return
	}
	files, _ := workspaceFiles(s.Workspace.Path, 2)
	writeJSON(w, map[string]any{"selected": true, "repo": s.Workspace.Repo, "ref": s.Workspace.Ref, "files": files})
}

func githubSelectWorkspaceAPI(w http.ResponseWriter, r *http.Request) {
	s, ok := githubSessionFor(r)
	if !ok {
		http.Error(w, "请先授权 GitHub", http.StatusUnauthorized)
		return
	}
	var in struct {
		Repo string `json:"repo"`
		Ref  string `json:"ref"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in); err != nil || !githubRepoName.MatchString(in.Repo) {
		http.Error(w, "项目名称无效", http.StatusBadRequest)
		return
	}
	if in.Ref == "" {
		in.Ref = "main"
	}
	base := filepath.Join(githubWorkspaceRoot(), safeWorkspaceID(in.Repo))
	if err := os.RemoveAll(base); err != nil {
		http.Error(w, "无法清理项目工作区", http.StatusInternalServerError)
		return
	}
	if err := os.MkdirAll(filepath.Dir(base), 0o700); err != nil {
		http.Error(w, "无法创建项目工作区", http.StatusInternalServerError)
		return
	}
	if err := gitClone(r.Context(), s.Token, "https://github.com/"+in.Repo+".git", in.Ref, base); err != nil {
		http.Error(w, "无法克隆 GitHub 项目: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.Workspace = &githubWorkspace{Repo: in.Repo, Ref: in.Ref, Path: base}
	files, _ := workspaceFiles(base, 2)
	writeJSON(w, map[string]any{"selected": true, "repo": in.Repo, "ref": in.Ref, "files": files})
}

func githubAgentAPI(w http.ResponseWriter, r *http.Request) {
	s, ok := githubSessionFor(r)
	if !ok {
		http.Error(w, "请先授权 GitHub", http.StatusUnauthorized)
		return
	}
	if s.Workspace == nil {
		http.Error(w, "请先选择 GitHub 项目", http.StatusBadRequest)
		return
	}
	var in struct {
		Model  string `json:"model"`
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil || strings.TrimSpace(in.Prompt) == "" || strings.TrimSpace(in.Model) == "" {
		http.Error(w, "模型和修改要求不能为空", http.StatusBadRequest)
		return
	}
	result, err := runGitHubAgent(r.Context(), s.Workspace.Path, in.Model, in.Prompt)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, result)
}

func githubPullRequestAPI(w http.ResponseWriter, r *http.Request) {
	s, ok := githubSessionFor(r)
	if !ok {
		http.Error(w, "请先授权 GitHub", http.StatusUnauthorized)
		return
	}
	if s.Workspace == nil {
		http.Error(w, "请先选择 GitHub 项目", http.StatusBadRequest)
		return
	}
	var in struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&in); err != nil || strings.TrimSpace(in.Title) == "" {
		http.Error(w, "Pull Request 标题不能为空", http.StatusBadRequest)
		return
	}
	branch := "everyapi/" + time.Now().UTC().Format("20060102-150405") + "-" + randomShortID()
	if err := gitRun(r.Context(), s.Workspace.Path, "checkout", "-b", branch); err != nil {
		http.Error(w, "无法创建修改分支", http.StatusBadGateway)
		return
	}
	if out, err := gitRunOutput(r.Context(), s.Workspace.Path, "status", "--porcelain"); err != nil || strings.TrimSpace(out) == "" {
		http.Error(w, "当前项目没有待提交修改", http.StatusBadRequest)
		return
	}
	if gitRun(r.Context(), s.Workspace.Path, "config", "user.name", "EveryAPI Gateway") != nil || gitRun(r.Context(), s.Workspace.Path, "config", "user.email", "gateway@everyapi.ai") != nil || gitRun(r.Context(), s.Workspace.Path, "add", "--all") != nil || gitRun(r.Context(), s.Workspace.Path, "commit", "-m", in.Title) != nil || gitPush(r.Context(), s.Token, s.Workspace.Path, branch) != nil {
		http.Error(w, "无法推送修改到 GitHub", http.StatusBadGateway)
		return
	}
	pr, err := githubCreatePR(r.Context(), s.Token, s.Workspace.Repo, map[string]string{"title": in.Title, "body": in.Body, "head": branch, "base": s.Workspace.Ref})
	if err != nil {
		http.Error(w, "无法创建 Pull Request", http.StatusBadGateway)
		return
	}
	writeJSON(w, pr)
}

func githubWorkspaceRoot() string {
	if d := strings.TrimSpace(os.Getenv("EVERYAPI_DATA_DIR")); d != "" {
		return filepath.Join(d, "github-workspaces")
	}
	return filepath.Join(settings.Dir(), "github-workspaces")
}

func safeWorkspaceID(repo string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return strings.ReplaceAll(repo, "/", "-") + "-" + hex.EncodeToString(b)
}

func githubJSON(ctx context.Context, token, method, endpoint string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, rd)
	if err != nil {
		return err
	}
	githubHeaders(req, token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return fmt.Errorf("github returned %s: %s", res.Status, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(out)
}

func githubCreatePR(ctx context.Context, token, repo string, body map[string]string) (map[string]any, error) {
	out := map[string]any{}
	err := githubJSON(ctx, token, http.MethodPost, "https://api.github.com/repos/"+url.PathEscape(strings.Split(repo, "/")[0])+"/"+url.PathEscape(strings.Split(repo, "/")[1])+"/pulls", body, &out)
	return out, err
}

func workspaceFiles(root string, depth int) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if rel == ".git" || strings.HasPrefix(rel, ".git"+string(filepath.Separator)) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() && strings.Count(filepath.ToSlash(rel), "/") >= depth {
			return filepath.SkipDir
		}
		if !d.IsDir() {
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

func safeWorkspacePath(root, name string) (string, error) {
	name = filepath.Clean(filepath.FromSlash(name))
	if name == "." || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
		return "", errors.New("invalid file path")
	}
	p := filepath.Join(root, name)
	r, err := filepath.Rel(root, p)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", errors.New("invalid file path")
	}
	base := strings.ToLower(filepath.Base(name))
	if base == ".env" || strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") || strings.Contains(base, "secret") || strings.Contains(base, "credential") {
		return "", errors.New("refusing to access a secret file")
	}
	return p, nil
}

func gitAskpass(token string) (string, func()) {
	f, err := os.CreateTemp("", "everyapi-git-askpass-")
	if err != nil {
		return "", func() {}
	}
	path := f.Name()
	_ = f.Chmod(0o700)
	_, _ = f.WriteString("#!/bin/sh\ncase \"$1\" in *[Uu]sername*) printf '%s\\n' oauth2;; *) printf '%s\\n' \"$EVERYAPI_GIT_TOKEN\";; esac\n")
	_ = f.Close()
	return path, func() { _ = os.Remove(path) }
}

func gitCommand(ctx context.Context, root, token string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS_REQUIRE=force", "EVERYAPI_GIT_TOKEN="+token)
	return cmd
}

func gitClone(ctx context.Context, token, remote, ref, dest string) error {
	ask, cleanup := gitAskpass(token)
	defer cleanup()
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", "--branch", ref, remote, dest)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS_REQUIRE=force", "GIT_ASKPASS="+ask, "EVERYAPI_GIT_TOKEN="+token)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}

func gitRun(ctx context.Context, root string, args ...string) error {
	return gitRunToken(ctx, root, "", args...)
}
func gitRunToken(ctx context.Context, root, token string, args ...string) error {
	return gitCommand(ctx, root, token, args...).Run()
}
func gitRunOutput(ctx context.Context, root string, args ...string) (string, error) {
	c := gitCommand(ctx, root, "", args...)
	b, err := c.Output()
	return string(b), err
}
func gitPush(ctx context.Context, token, root, branch string) error {
	ask, cleanup := gitAskpass(token)
	defer cleanup()
	c := gitCommand(ctx, root, token, "push", "-u", "origin", branch)
	c.Env = append(c.Env, "GIT_ASKPASS="+ask)
	c.Stdout, c.Stderr = io.Discard, io.Discard
	return c.Run()
}

func randomShortID() string { b := make([]byte, 4); _, _ = rand.Read(b); return hex.EncodeToString(b) }

type githubToolCall struct {
	ID, Name  string
	Arguments json.RawMessage
}

func runGitHubAgent(ctx context.Context, root, model, prompt string) (map[string]any, error) {
	tools := []any{
		map[string]any{"type": "function", "function": map[string]any{"name": "list_files", "description": "List files in the project", "parameters": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "additionalProperties": false}}},
		map[string]any{"type": "function", "function": map[string]any{"name": "read_file", "description": "Read a text file", "parameters": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []string{"path"}, "additionalProperties": false}}},
		map[string]any{"type": "function", "function": map[string]any{"name": "write_file", "description": "Write a text file to implement the requested change", "parameters": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"}}, "required": []string{"path", "content"}, "additionalProperties": false}}},
		map[string]any{"type": "function", "function": map[string]any{"name": "git_diff", "description": "Show current changes", "parameters": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}},
	}
	messages := []any{map[string]any{"role": "system", "content": "You are a careful coding agent. Work only inside the selected repository. Inspect files before editing. Use write_file for requested changes. Never modify .git or secrets. Explain the changes when done."}, map[string]any{"role": "user", "content": prompt}}
	for step := 0; step < 10; step++ {
		body := map[string]any{"model": model, "messages": messages, "tools": tools, "tool_choice": "auto", "stream": false}
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(b)).WithContext(ctx)
		// The standalone distribution requires an issued client key even on
		// loopback. This request never leaves the process, so give it a
		// bounded internal identity instead of reading a user's key from disk
		// or making the GitHub workflow depend on a revocable client key.
		req = req.WithContext(access.WithIdentity(req.Context(), access.Identity{
			KeyID:   "everyapi-github-agent",
			KeyName: "EveryAPI GitHub Agent",
		}))
		rec := httptest.NewRecorder()
		gw := served.Load()
		if gw == nil {
			return nil, errors.New("EveryAPI gateway unavailable")
		}
		gw.Handler().ServeHTTP(rec, req)
		if rec.Code < 200 || rec.Code >= 300 {
			return nil, fmt.Errorf("EveryAPI 请求失败: %s", rec.Body.String())
		}
		var out struct {
			Choices []struct {
				Message struct {
					Role      string `json:"role"`
					Content   string `json:"content"`
					ToolCalls []struct {
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Choices) == 0 {
			return nil, errors.New("EveryAPI 返回格式无效")
		}
		m := out.Choices[0].Message
		assistant := map[string]any{"role": "assistant", "content": m.Content}
		var calls []githubToolCall
		for _, c := range m.ToolCalls {
			calls = append(calls, githubToolCall{ID: c.ID, Name: c.Function.Name, Arguments: json.RawMessage(c.Function.Arguments)})
		}
		if len(calls) == 0 {
			return map[string]any{"message": m.Content, "files": changedFiles(ctx, root), "diff": gitDiff(ctx, root)}, nil
		}
		assistant["tool_calls"] = m.ToolCalls
		messages = append(messages, assistant)
		for _, call := range calls {
			result := githubToolResult(root, call)
			messages = append(messages, map[string]any{"role": "tool", "tool_call_id": call.ID, "content": result})
		}
	}
	return nil, errors.New("修改步骤过多，请缩小任务范围")
}

func githubToolResult(root string, call githubToolCall) string {
	var in struct{ Path, Content string }
	_ = json.Unmarshal(call.Arguments, &in)
	switch call.Name {
	case "list_files":
		files, err := workspaceFiles(root, 6)
		if err != nil {
			return err.Error()
		}
		return strings.Join(files, "\n")
	case "read_file":
		p, err := safeWorkspacePath(root, in.Path)
		if err != nil {
			return err.Error()
		}
		b, err := os.ReadFile(p)
		if err != nil || len(b) > 1<<20 {
			return "cannot read file"
		}
		return string(b)
	case "write_file":
		p, err := safeWorkspacePath(root, in.Path)
		if err != nil {
			return err.Error()
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err.Error()
		}
		if len(in.Content) > 2<<20 {
			return "file too large"
		}
		if err := os.WriteFile(p, []byte(in.Content), 0o600); err != nil {
			return err.Error()
		}
		return "written"
	case "git_diff":
		return gitDiff(context.Background(), root)
	default:
		return "unknown tool"
	}
}

func changedFiles(ctx context.Context, root string) []string {
	out, _ := gitRunOutput(ctx, root, "status", "--porcelain")
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if len(line) > 3 {
			files = append(files, strings.TrimSpace(line[3:]))
		}
	}
	return files
}
func gitDiff(ctx context.Context, root string) string {
	out, _ := gitRunOutput(ctx, root, "diff", "--", ".")
	if len(out) > 1<<20 {
		return out[:1<<20]
	}
	return out
}
