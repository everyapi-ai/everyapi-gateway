package gui

// GitHub project access for the EveryAPI web gateway. OAuth sessions and
// workspace metadata are encrypted in the private data directory so a restart
// does not force another authorization.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
	"unicode/utf8"

	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/settings"
)

type githubSession struct {
	mu              sync.Mutex
	id, binding     string
	Token, Username string
	RefreshToken    string
	Expires         time.Time
	TokenExpires    time.Time
	RefreshExpires  time.Time
	Workspace       *githubWorkspace
	Running         bool
	AgentError      string
	Activity        []string
	Cancel          context.CancelFunc
	Invalid         bool
	disconnected    bool
	persistPending  bool
}

type githubWorkspace struct {
	Repo, Ref, Path string
	Branch          string
	PullRequest     map[string]any
	LastPrompt      string
	LastResult      map[string]any
	History         []any
}

var githubSessions sync.Map // session id -> *githubSession

var githubRepoName = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func githubSessionFor(r *http.Request) (*githubSession, bool) {
	c, err := r.Cookie(githubSessionCookie)
	if err != nil || c.Value == "" {
		return nil, false
	}
	s, err := loadGitHubSession(c.Value)
	if err != nil || s == nil {
		githubSessions.Delete(c.Value)
		return nil, false
	}
	s.mu.Lock()
	invalid := s.Invalid || time.Now().After(s.Expires)
	s.mu.Unlock()
	if invalid {
		return nil, false
	}
	return s, true
}

func githubProjectRoutes(mux *http.ServeMux) {
	if !WebEveryAPIOnly {
		return
	}
	mux.HandleFunc("GET /api/github/status", githubStatusAPI)
	mux.HandleFunc("POST /api/github/config", githubConfigAPI)
	mux.HandleFunc("GET /api/github/repos", githubReposAPI)
	mux.HandleFunc("GET /api/github/workspace", githubWorkspaceAPI)
	mux.HandleFunc("POST /api/github/workspace", githubSelectWorkspaceAPI)
	mux.HandleFunc("GET /api/github/file", githubWorkspaceFileAPI)
	mux.HandleFunc("POST /api/github/agent", githubAgentAPI)
	mux.HandleFunc("GET /api/github/agent", githubAgentStatusAPI)
	mux.HandleFunc("DELETE /api/github/agent", githubCancelAgentAPI)
	mux.HandleFunc("POST /api/github/pull-request", githubPullRequestAPI)
	mux.HandleFunc("POST /api/github/disconnect", githubDisconnectAPI)
}

func githubStatusAPI(w http.ResponseWriter, r *http.Request) {
	cfg := loadWebGitHubConfig()
	_, connected := githubSessionFor(r)
	writeJSON(w, map[string]any{"connected": connected, "configured": cfg.enabled(), "organization": cfg.Organization, "callback_url": githubCallbackURL(r), "managed": strings.TrimSpace(os.Getenv("EVERYAPI_GITHUB_OAUTH_CLIENT_ID")) != "" || strings.TrimSpace(os.Getenv("EVERYAPI_GITHUB_CLIENT_ID")) != ""})
}

func githubConfigAPI(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(os.Getenv("EVERYAPI_GITHUB_OAUTH_CLIENT_ID")) != "" || strings.TrimSpace(os.Getenv("EVERYAPI_GITHUB_CLIENT_ID")) != "" {
		http.Error(w, "GitHub OAuth 由部署配置管理，请更新部署中的凭据", http.StatusConflict)
		return
	}
	var in githubOAuthStoredConfig
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		http.Error(w, "GitHub OAuth 配置无效", http.StatusBadRequest)
		return
	}
	in.ClientID = strings.TrimSpace(in.ClientID)
	in.ClientSecret = strings.TrimSpace(in.ClientSecret)
	in.Organization = strings.TrimSpace(in.Organization)
	if !validGitHubConfigValue(in.ClientID) || !validGitHubConfigValue(in.ClientSecret) || !validGitHubConfigValue(in.Organization) {
		http.Error(w, "Client ID、Client Secret 和组织名不能为空且不能包含空白字符", http.StatusBadRequest)
		return
	}
	if err := saveStoredGitHubConfig(in); err != nil {
		http.Error(w, "无法保存 GitHub OAuth 配置", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"configured": true, "organization": in.Organization})
}

func githubReposAPI(w http.ResponseWriter, r *http.Request) {
	s, ok := githubSessionFor(r)
	if !ok {
		http.Error(w, "请先授权 GitHub", http.StatusUnauthorized)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := refreshGitHubSessionLocked(ctx, s); err != nil {
		githubSessionError(w, s, err, "GitHub 连接暂时不可用，请重试")
		return
	}
	type repository struct {
		FullName      string `json:"full_name"`
		Name          string `json:"name"`
		Private       bool   `json:"private"`
		DefaultBranch string `json:"default_branch"`
		Description   string `json:"description"`
		Updated       string `json:"updated_at"`
		Archived      bool   `json:"archived"`
		Permissions   struct {
			Push bool `json:"push"`
		} `json:"permissions"`
	}
	repos := []repository{}
	for page := 1; ; page++ {
		var batch []repository
		endpoint := fmt.Sprintf("https://api.github.com/user/repos?per_page=100&sort=updated&affiliation=owner%%2Ccollaborator%%2Corganization_member&page=%d", page)
		if err := githubJSON(ctx, s.Token, http.MethodGet, endpoint, nil, &batch); err != nil {
			githubSessionError(w, s, err, "无法读取 GitHub 项目列表")
			return
		}
		for _, repo := range batch {
			if repo.Permissions.Push && !repo.Archived {
				repos = append(repos, repo)
			}
		}
		if len(batch) < 100 {
			break
		}
	}
	writeJSON(w, map[string]any{"user": s.Username, "repos": repos})
}

func githubWorkspaceAPI(w http.ResponseWriter, r *http.Request) {
	s, ok := githubSessionFor(r)
	if !ok {
		http.Error(w, "请先授权 GitHub", http.StatusUnauthorized)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Workspace == nil {
		writeJSON(w, map[string]any{"selected": false})
		return
	}
	ws := s.Workspace
	files, _ := workspaceFiles(ws.Path, 0)
	writeJSON(w, map[string]any{"selected": true, "repo": ws.Repo, "ref": ws.Ref, "files": files, "prompt": ws.LastPrompt, "result": ws.LastResult, "pull_request": ws.PullRequest, "running": s.Running, "error": s.AgentError})
}

func githubWorkspaceFileAPI(w http.ResponseWriter, r *http.Request) {
	s, ok := githubSessionFor(r)
	if !ok {
		http.Error(w, "请先授权 GitHub", http.StatusUnauthorized)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Workspace == nil {
		http.Error(w, "请先选择 GitHub 项目", http.StatusBadRequest)
		return
	}
	p, err := safeWorkspacePath(s.Workspace.Path, r.URL.Query().Get("path"))
	if err != nil {
		http.Error(w, "无法访问此文件", http.StatusBadRequest)
		return
	}
	root, err := os.OpenRoot(s.Workspace.Path)
	if err != nil {
		http.Error(w, "无法打开工作区", http.StatusInternalServerError)
		return
	}
	defer root.Close()
	name, _ := filepath.Rel(s.Workspace.Path, p)
	f, err := root.Open(name)
	if err != nil {
		http.Error(w, "无法读取文件", http.StatusNotFound)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.Error(w, "只能预览文本文件", http.StatusBadRequest)
		return
	}
	const limit = 256 << 10
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		http.Error(w, "无法读取文件", http.StatusInternalServerError)
		return
	}
	truncated := len(b) > limit
	if truncated {
		cut := limit
		for cut > 0 && !utf8.RuneStart(b[cut]) {
			cut--
		}
		b = b[:cut]
	}
	if !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
		http.Error(w, "此文件不是可预览的 UTF-8 文本", http.StatusUnsupportedMediaType)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{"path": filepath.ToSlash(name), "content": string(b), "truncated": truncated})
}

func githubSelectWorkspaceAPI(w http.ResponseWriter, r *http.Request) {
	s, ok := githubSessionFor(r)
	if !ok {
		http.Error(w, "请先授权 GitHub", http.StatusUnauthorized)
		return
	}
	if !s.mu.TryLock() {
		http.Error(w, "项目正在处理其他请求，请稍后重试", http.StatusConflict)
		return
	}
	defer s.mu.Unlock()
	if s.Running {
		http.Error(w, "项目正在修改中，请完成后再切换", http.StatusConflict)
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
	if s.Workspace != nil && s.Workspace.Repo == in.Repo && s.Workspace.Ref == in.Ref {
		files, _ := workspaceFiles(s.Workspace.Path, 0)
		writeJSON(w, map[string]any{"selected": true, "repo": in.Repo, "ref": in.Ref, "files": files})
		return
	}
	if s.Workspace != nil && (len(changedFiles(r.Context(), s.Workspace.Path)) > 0 || (s.Workspace.Branch != "" && s.Workspace.PullRequest == nil)) {
		http.Error(w, "当前项目有未提交修改，请先创建 Pull Request", http.StatusConflict)
		return
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
	if err := refreshGitHubSessionLocked(r.Context(), s); err != nil {
		githubSessionError(w, s, err, "GitHub 连接暂时不可用，请重试")
		return
	}
	if err := gitClone(r.Context(), s.Token, "https://github.com/"+in.Repo+".git", in.Ref, base); err != nil {
		_ = os.RemoveAll(base)
		http.Error(w, "无法克隆 GitHub 项目: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.Workspace = &githubWorkspace{Repo: in.Repo, Ref: in.Ref, Path: base}
	if err := saveGitHubSessionLocked(s); err != nil {
		http.Error(w, "无法保存项目连接状态", http.StatusInternalServerError)
		return
	}
	files, _ := workspaceFiles(base, 0)
	writeJSON(w, map[string]any{"selected": true, "repo": in.Repo, "ref": in.Ref, "files": files})
}

func githubAgentAPI(w http.ResponseWriter, r *http.Request) {
	s, ok := githubSessionFor(r)
	if !ok {
		http.Error(w, "请先授权 GitHub", http.StatusUnauthorized)
		return
	}
	if !s.mu.TryLock() {
		http.Error(w, "项目正在处理其他请求，请稍后重试", http.StatusConflict)
		return
	}
	defer s.mu.Unlock()
	if s.Running {
		http.Error(w, "项目正在修改中，请等待当前任务完成", http.StatusConflict)
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
	ws := s.Workspace
	ws.LastPrompt, ws.LastResult = in.Prompt, nil
	s.Running, s.AgentError = true, ""
	s.Activity = []string{"任务已接受，等待模型分析…"}
	if err := saveGitHubSessionLocked(s); err != nil {
		s.Running = false
		http.Error(w, "无法保存任务状态，请重试", 500)
		return
	}
	taskWorkspace := *ws
	taskWorkspace.History = append([]any(nil), ws.History...)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	s.Cancel = cancel
	// A mobile browser can disconnect while the model works. Keep the bounded task on the server and expose its result through GET so a refresh resumes it.
	go func() {
		defer cancel()
		result, err := runGitHubAgent(ctx, &taskWorkspace, in.Model, in.Prompt, func(message string) {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.Activity = append(s.Activity, message)
			if len(s.Activity) > 100 {
				s.Activity = s.Activity[len(s.Activity)-100:]
			}
		})
		s.mu.Lock()
		defer s.mu.Unlock()
		s.Running = false
		s.Cancel = nil
		if err != nil {
			s.AgentError = err.Error()
			if errors.Is(ctx.Err(), context.Canceled) {
				s.AgentError = "任务已停止，已有文件修改保留。"
			}
			reviewCtx, reviewCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer reviewCancel()
			result = map[string]any{"message": "任务未完成，请检查已有修改后重试。", "files": changedFiles(reviewCtx, ws.Path), "diff": gitDiff(reviewCtx, ws.Path)}
		}
		ws.LastResult = result
		ws.History = taskWorkspace.History
		if err := saveGitHubSessionLocked(s); err != nil {
			s.AgentError = "无法保存任务结果，刷新前请先检查已有修改。"
		}
	}()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]any{"running": true})
}

func githubAgentStatusAPI(w http.ResponseWriter, r *http.Request) {
	s, ok := githubSessionFor(r)
	if !ok {
		http.Error(w, "请先授权 GitHub", http.StatusUnauthorized)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var result map[string]any
	if s.Workspace != nil {
		result = s.Workspace.LastResult
	}
	writeJSON(w, map[string]any{"running": s.Running, "error": s.AgentError, "result": result, "activity": s.Activity})
}

func githubCancelAgentAPI(w http.ResponseWriter, r *http.Request) {
	s, ok := githubSessionFor(r)
	if !ok {
		http.Error(w, "请先授权 GitHub", http.StatusUnauthorized)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Cancel != nil {
		s.Cancel()
	}
	writeJSON(w, map[string]any{"running": s.Running})
}

func githubPullRequestAPI(w http.ResponseWriter, r *http.Request) {
	s, ok := githubSessionFor(r)
	if !ok {
		http.Error(w, "请先授权 GitHub", http.StatusUnauthorized)
		return
	}
	if !s.mu.TryLock() {
		http.Error(w, "项目正在处理其他请求，请稍后重试", http.StatusConflict)
		return
	}
	defer s.mu.Unlock()
	if s.Running {
		http.Error(w, "项目正在修改中，请完成后再创建 Pull Request", http.StatusConflict)
		return
	}
	if s.Workspace == nil {
		http.Error(w, "请先选择 GitHub 项目", http.StatusBadRequest)
		return
	}
	if err := refreshGitHubSessionLocked(r.Context(), s); err != nil {
		githubSessionError(w, s, err, "GitHub 连接暂时不可用，请重试")
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
	ws := s.Workspace
	out, err := gitRunOutput(r.Context(), ws.Path, "status", "--porcelain")
	if err != nil {
		http.Error(w, "无法检查项目修改", http.StatusBadGateway)
		return
	}
	dirty := out != ""
	if !dirty && ws.PullRequest != nil {
		writeJSON(w, ws.PullRequest)
		return
	}
	if !dirty && ws.Branch == "" {
		http.Error(w, "当前项目没有待提交修改", http.StatusBadRequest)
		return
	}
	if ws.Branch == "" {
		branch := "everyapi/" + time.Now().UTC().Format("20060102-150405") + "-" + randomShortID()
		if err := gitRun(r.Context(), ws.Path, "checkout", "-b", branch); err != nil {
			http.Error(w, "无法创建修改分支", http.StatusBadGateway)
			return
		}
		ws.Branch = branch
		if err := saveGitHubSessionLocked(s); err != nil {
			http.Error(w, "无法保存分支状态，请重试", 500)
			return
		}
	}
	if dirty && (gitRun(r.Context(), ws.Path, "config", "user.name", "EveryAPI Gateway") != nil || gitRun(r.Context(), ws.Path, "config", "user.email", "gateway@everyapi.ai") != nil || gitRun(r.Context(), ws.Path, "add", "--all") != nil || gitRun(r.Context(), ws.Path, "commit", "-m", in.Title) != nil) {
		http.Error(w, "无法提交项目修改，可以重试", http.StatusBadGateway)
		return
	}
	if gitPush(r.Context(), s.Token, ws.Path, ws.Branch) != nil {
		http.Error(w, "无法推送修改到 GitHub", http.StatusBadGateway)
		return
	}
	if ws.PullRequest != nil {
		writeJSON(w, ws.PullRequest)
		return
	}
	pr, err := githubCreatePR(r.Context(), s.Token, ws.Repo, map[string]string{"title": in.Title, "body": in.Body, "head": ws.Branch, "base": ws.Ref})
	if err != nil {
		githubSessionError(w, s, err, "无法创建 Pull Request")
		return
	}
	ws.PullRequest = pr
	if err := saveGitHubSessionLocked(s); err != nil {
		http.Error(w, "无法保存 Pull Request 状态，请重试", 500)
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
		return &githubHTTPError{Status: res.StatusCode, Detail: fmt.Sprintf("%s: %s", res.Status, strings.TrimSpace(string(b)))}
	}
	return json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(out)
}

func githubCreatePR(ctx context.Context, token, repo string, body map[string]string) (map[string]any, error) {
	out := map[string]any{}
	err := githubJSON(ctx, token, http.MethodPost, "https://api.github.com/repos/"+url.PathEscape(strings.Split(repo, "/")[0])+"/"+url.PathEscape(strings.Split(repo, "/")[1])+"/pulls", body, &out)
	if err != nil {
		// A successful creation may have lost its response; a retry must return the existing PR rather than stay stuck on GitHub's duplicate-head error.
		q := url.Values{"head": {strings.Split(repo, "/")[0] + ":" + body["head"]}, "base": {body["base"]}, "state": {"open"}}
		var existing []map[string]any
		if lookupErr := githubJSON(ctx, token, http.MethodGet, "https://api.github.com/repos/"+repo+"/pulls?"+q.Encode(), nil, &existing); lookupErr == nil && len(existing) > 0 {
			return existing[0], nil
		}
	}
	return out, err
}

func workspaceFiles(root string, depth int) ([]string, error) {
	var out []string
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	err = fs.WalkDir(r.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		if _, err := workspaceFileName(name); err != nil || d.Type()&os.ModeSymlink != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if depth > 0 && d.IsDir() && strings.Count(name, "/") >= depth {
			return filepath.SkipDir
		}
		if !d.IsDir() {
			out = append(out, name)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

func workspaceFileName(name string) (string, error) {
	name = filepath.Clean(filepath.FromSlash(name))
	if !filepath.IsLocal(name) || name == "." {
		return "", errors.New("invalid file path")
	}
	for _, part := range strings.Split(filepath.ToSlash(name), "/") {
		if strings.EqualFold(part, ".git") {
			return "", errors.New("refusing to access git metadata")
		}
	}
	base := strings.ToLower(filepath.Base(name))
	if base == ".env" || strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") || strings.Contains(base, "secret") || strings.Contains(base, "credential") {
		return "", errors.New("refusing to access a secret file")
	}
	return name, nil
}

func safeWorkspacePath(root, name string) (string, error) {
	name, err := workspaceFileName(name)
	if err != nil {
		return "", err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer r.Close()
	current := ""
	for _, part := range strings.Split(filepath.ToSlash(name), "/") {
		current = filepath.Join(current, part)
		info, err := r.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return "", errors.New("refusing to access a symlink or special file")
		}
	}
	return filepath.Join(root, name), nil
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
	cmd := proc.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS_REQUIRE=force", "EVERYAPI_GIT_TOKEN="+token)
	return cmd
}

func gitClone(ctx context.Context, token, remote, ref, dest string) error {
	ask, cleanup := gitAskpass(token)
	defer cleanup()
	cmd := proc.CommandContext(ctx, "git", "clone", "--depth", "1", "--branch", ref, remote, dest)
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

func runGitHubAgent(ctx context.Context, ws *githubWorkspace, model, prompt string, report func(string)) (map[string]any, error) {
	root := ws.Path
	tools := []any{
		map[string]any{"type": "function", "function": map[string]any{"name": "edit_file", "description": "Replace exactly one occurrence of old_text in an existing file with new_text. Read the file first and use a unique exact snippet including context. Prefer this for focused edits instead of rewriting the whole file.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}, "old_text": map[string]any{"type": "string"}, "new_text": map[string]any{"type": "string"}}, "required": []string{"path", "old_text", "new_text"}, "additionalProperties": false}}},
		map[string]any{"type": "function", "function": map[string]any{"name": "search_files", "description": "Find project file paths containing query, such as 'github', 'sessions' or 'README'. Use this to locate named source files instead of recursively listing directories. Results are paginated with next_offset.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}, "offset": map[string]any{"type": "integer", "minimum": 0}}, "required": []string{"query"}, "additionalProperties": false}}},
		map[string]any{"type": "function", "function": map[string]any{"name": "list_files", "description": "List one directory, starting at path '.' for the root. Directories end with '/'. Use next_offset as offset for another page, or choose a subdirectory. Returns at most 100 entries.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}, "offset": map[string]any{"type": "integer", "minimum": 0}}, "additionalProperties": false}}},
		map[string]any{"type": "function", "function": map[string]any{"name": "read_file", "description": "Read up to 16 KiB of a text file. Large files return content and next_offset; pass that byte offset to continue.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}, "offset": map[string]any{"type": "integer", "minimum": 0}}, "required": []string{"path"}, "additionalProperties": false}}},
		map[string]any{"type": "function", "function": map[string]any{"name": "write_file", "description": "Write a text file to implement the requested change", "parameters": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"}}, "required": []string{"path", "content"}, "additionalProperties": false}}},
		map[string]any{"type": "function", "function": map[string]any{"name": "git_diff", "description": "Show current changes", "parameters": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}},
	}
	messages := []any{map[string]any{"role": "system", "content": "You are a careful coding agent. Work only inside the selected repository. Inspect files before editing, staying focused on the requested files and behavior. For questions about specific files, read those files and answer directly; do not explore unrelated implementation details. Reuse earlier tool results rather than rereading unchanged files. Use search_files only to locate filenames, not to search file contents or function names. Prefer edit_file for focused changes and write_file for new files. Review the diff, then explain the changes when done. Never modify .git or secrets. You cannot execute commands or run tests; do not claim tests passed."}}
	history := ws.History
	for len(history) > 0 {
		b, _ := json.Marshal(history)
		if len(b) <= 16<<10 {
			break
		}
		history = history[2:]
	}
	messages = append(messages, history...)
	messages = append(messages, map[string]any{"role": "user", "content": prompt})
	repeatedTools := map[string]int{}
	forceFinal := false
	for step := 0; step < 32; step++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		report(fmt.Sprintf("正在分析 · 第 %d 步", step+1))
		messages = boundGitHubAgentMessages(messages)
		choice := any("auto")
		if step == 31 {
			forceFinal = true
		}
		if forceFinal {
			choice = "none"
			messages = append(messages, map[string]any{"role": "system", "content": "Finish now using the evidence already collected. Do not call tools. Answer the user's original request directly, and clearly state any incomplete work or uncertainty. Do not claim that unperformed edits or tests succeeded."})
		}
		body := map[string]any{"model": model, "messages": messages, "tools": tools, "tool_choice": choice, "stream": false}
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(b)).WithContext(ctx)
		// The standalone distribution requires an issued client key even on
		// loopback. This request never leaves the process, so give it a
		// bounded internal identity instead of reading a user's key from disk
		// or making the GitHub workflow depend on a revocable client key.
		req = req.WithContext(internalGatewayContext(req.Context()))
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
						Type     string `json:"type"`
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
			ws.History = append(ws.History, map[string]any{"role": "user", "content": prompt}, assistant)
			if len(ws.History) > 20 {
				ws.History = ws.History[len(ws.History)-20:]
			}
			return map[string]any{"message": m.Content, "files": changedFiles(ctx, root), "diff": gitDiff(ctx, root)}, nil
		}
		if forceFinal {
			return nil, errors.New("模型未能结束任务，请换一个模型重试；已有修改保留。")
		}
		assistant["tool_calls"] = m.ToolCalls
		messages = append(messages, assistant)
		for _, call := range calls {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			var target struct {
				Path string `json:"path"`
			}
			_ = json.Unmarshal(call.Arguments, &target)
			report(call.Name + " " + target.Path)
			result := githubToolResult(root, call)
			var arguments any
			_ = json.Unmarshal(call.Arguments, &arguments)
			canonical, _ := json.Marshal(arguments)
			resultHash := sha256.Sum256([]byte(result))
			callKey := fmt.Sprintf("%s:%s:%x", call.Name, canonical, resultHash)
			repeatedTools[callKey]++
			if repeatedTools[callKey] >= 3 {
				forceFinal = true
				report("工具调用重复且结果未变化，正在整理已有结果…")
			}
			messages = append(messages, map[string]any{"role": "tool", "tool_call_id": call.ID, "content": result})
		}
	}
	return nil, errors.New("修改步骤过多，请缩小任务范围")
}

// Keep complete assistant/tool exchanges together when removing old context. Removing only a tool message would leave an unanswered tool call in the model request.
func boundGitHubAgentMessages(messages []any) []any {
	for {
		b, _ := json.Marshal(messages)
		if len(b) <= 80<<10 {
			return messages
		}
		removed := false
		for i := 1; i < len(messages); i++ {
			message, _ := messages[i].(map[string]any)
			if message["role"] != "assistant" || message["tool_calls"] == nil {
				continue
			}
			end := i + 1
			for end < len(messages) {
				m, _ := messages[end].(map[string]any)
				if m["role"] != "tool" {
					break
				}
				end++
			}
			if end == i+1 {
				continue
			}
			messages = append(messages[:i], messages[end:]...)
			removed = true
			break
		}
		if !removed {
			return messages
		}
	}
}

func githubToolResult(root string, call githubToolCall) string {
	var in struct {
		Path, Content, Query string
		Offset               int
		OldText              string `json:"old_text"`
		NewText              string `json:"new_text"`
	}
	if err := json.Unmarshal(call.Arguments, &in); err != nil || in.Offset < 0 {
		return "invalid tool arguments"
	}
	switch call.Name {
	case "edit_file":
		if in.OldText == "" {
			return "old_text must not be empty"
		}
		p, err := safeWorkspacePath(root, in.Path)
		if err != nil {
			return err.Error()
		}
		r, err := os.OpenRoot(root)
		if err != nil {
			return "cannot open workspace"
		}
		defer r.Close()
		name, _ := filepath.Rel(root, p)
		f, err := r.Open(name)
		if err != nil {
			return "cannot read file"
		}
		b, err := io.ReadAll(io.LimitReader(f, (2<<20)+1))
		f.Close()
		if err != nil || len(b) > 2<<20 || !utf8.Valid(b) {
			return "cannot edit file: UTF-8 text up to 2 MiB is required"
		}
		text := string(b)
		if strings.Count(text, in.OldText) != 1 {
			return "old_text must match exactly once; read the file and include more surrounding context"
		}
		text = strings.Replace(text, in.OldText, in.NewText, 1)
		if len(text) > 2<<20 {
			return "file too large"
		}
		if err := r.WriteFile(name, []byte(text), 0o600); err != nil {
			return "cannot write file"
		}
		return "edited"
	case "search_files":
		if strings.TrimSpace(in.Query) == "" || len(in.Query) > 200 {
			return "query must contain 1 to 200 bytes"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, err := gitRunOutput(ctx, root, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
		if err != nil {
			return "cannot search project files"
		}
		files := []string{}
		for _, name := range strings.Split(out, "\x00") {
			if _, err := workspaceFileName(name); err != nil || !strings.Contains(strings.ToLower(name), strings.ToLower(in.Query)) {
				continue
			}
			files = append(files, name)
		}
		sort.Strings(files)
		return githubFilePage(files, in.Offset)
	case "list_files":
		return workspaceDirectoryPage(root, in.Path, in.Offset)
	case "read_file":
		p, err := safeWorkspacePath(root, in.Path)
		if err != nil {
			return err.Error()
		}
		r, err := os.OpenRoot(root)
		if err != nil {
			return "cannot open workspace"
		}
		defer r.Close()
		name, _ := filepath.Rel(root, p)
		f, err := r.Open(name)
		if err != nil {
			return "cannot read file"
		}
		defer f.Close()
		if _, err := f.Seek(int64(in.Offset), io.SeekStart); err != nil {
			return "cannot read file offset"
		}
		const chunkSize = 16 << 10
		b, err := io.ReadAll(io.LimitReader(f, chunkSize+1))
		if err != nil {
			return "cannot read file"
		}
		if len(b) > chunkSize {
			cut := chunkSize
			for cut > 0 && !utf8.RuneStart(b[cut]) {
				cut--
			}
			if cut == 0 || !utf8.Valid(b[:cut]) {
				return "file is not UTF-8 text"
			}
			out, _ := json.Marshal(map[string]any{"content": string(b[:cut]), "next_offset": in.Offset + cut})
			return string(out)
		}
		if !utf8.Valid(b) {
			return "file is not UTF-8 text"
		}
		return string(b)
	case "write_file":
		p, err := safeWorkspacePath(root, in.Path)
		if err != nil {
			return err.Error()
		}
		if len(in.Content) > 2<<20 {
			return "file too large"
		}
		r, err := os.OpenRoot(root)
		if err != nil {
			return "cannot open workspace"
		}
		defer r.Close()
		name, _ := filepath.Rel(root, p)
		if err := r.MkdirAll(filepath.Dir(name), 0o700); err != nil {
			return err.Error()
		}
		if err := r.WriteFile(name, []byte(in.Content), 0o600); err != nil {
			return err.Error()
		}
		return "written"
	case "git_diff":
		diff := gitDiff(context.Background(), root)
		if len(diff) > 16<<10 {
			diff = diff[:16<<10] + "\n[Diff truncated for model input. Read individual changed files for details; the review UI retains the full diff.]"
		}
		return diff
	default:
		return "unknown tool"
	}
}

func workspaceDirectoryPage(root, name string, offset int) string {
	if name == "" {
		name = "."
	}
	if name != "." {
		p, err := safeWorkspacePath(root, name)
		if err != nil {
			return err.Error()
		}
		name, _ = filepath.Rel(root, p)
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return "cannot open workspace"
	}
	defer r.Close()
	entries, err := fs.ReadDir(r.FS(), filepath.ToSlash(name))
	if err != nil {
		return "cannot list directory"
	}
	files := []string{}
	for _, entry := range entries {
		p := filepath.Join(name, entry.Name())
		if _, err := workspaceFileName(p); err != nil || entry.Type()&os.ModeSymlink != 0 || (!entry.IsDir() && !entry.Type().IsRegular()) {
			continue
		}
		p = filepath.ToSlash(p)
		if entry.IsDir() {
			p += "/"
		}
		files = append(files, p)
	}
	return githubFilePage(files, offset)
}

func githubFilePage(files []string, offset int) string {
	if offset > len(files) {
		return "directory offset is out of range"
	}
	end, bytes := offset, 0
	for end < len(files) && end-offset < 100 {
		size := len(files[end])
		if bytes+size > 12<<10 {
			break
		}
		bytes += size
		end++
	}
	out := map[string]any{"entries": files[offset:end]}
	if end < len(files) {
		out["next_offset"] = end
	}
	b, _ := json.Marshal(out)
	return string(b)
}

func changedFiles(ctx context.Context, root string) []string {
	out, _ := gitRunOutput(ctx, root, "status", "--porcelain", "-z")
	var files []string
	entries := strings.Split(out, "\x00")
	for i := 0; i < len(entries); i++ {
		line := entries[i]
		if len(line) > 3 {
			files = append(files, line[3:])
			if strings.ContainsAny(line[:2], "RC") {
				i++ // In -z format a rename's old path follows its new path.
			}
		}
	}
	return files
}
func gitDiff(ctx context.Context, root string) string {
	out, _ := gitRunOutput(ctx, root, "diff", "HEAD", "--", ".")
	// git diff omits untracked files. Append their no-index patches so new files can be reviewed before the PR is created.
	if untracked, err := gitRunOutput(ctx, root, "ls-files", "--others", "--exclude-standard", "-z"); err == nil {
		for _, name := range strings.Split(untracked, "\x00") {
			if name == "" {
				continue
			}
			if _, err := safeWorkspacePath(root, name); err != nil {
				continue
			}
			patch, _ := gitRunOutput(ctx, root, "diff", "--no-index", "--", os.DevNull, name)
			if patch != "" {
				out += patch
			}
		}
	}
	if len(out) > 1<<20 {
		return out[:1<<20]
	}
	return out
}
