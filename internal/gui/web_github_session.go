package gui

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// A session cookie is a 192-bit random capability. Derive its encryption key
// separately from its filename so the volume alone cannot reveal OAuth tokens.
// Only one gateway process may use this data directory (the deployment uses Recreate).
var githubSessionLoadMu sync.Mutex

type githubSavedSession struct {
	Token, RefreshToken, Username         string
	Expires, TokenExpires, RefreshExpires time.Time
	Workspace                             *githubWorkspace
	Running, Invalid                      bool
	AgentError                            string
}

func githubSessionCipher(id string) (cipher.AEAD, error) {
	b, err := hex.DecodeString(id)
	if err != nil || len(b) != 24 {
		return nil, errors.New("invalid GitHub session id")
	}
	key := sha256.Sum256([]byte("everyapi-github-session-key-v1:" + id))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func githubSessionPath(id string) string {
	h := sha256.Sum256([]byte(id))
	return filepath.Join(filepath.Dir(githubOAuthConfigPath()), "github-sessions", hex.EncodeToString(h[:])+".session")
}

func githubSessionBinding() string {
	cfg := loadWebGitHubConfig()
	return "everyapi-github-session-v1:" + cfg.ClientID + ":" + strings.ToLower(cfg.Organization)
}

// saveGitHubSessionLocked requires s.mu. Agents work on a copy of History;
// their completed history is published under the same lock as their result.
func saveGitHubSessionLocked(s *githubSession) (err error) {
	if s.id == "" || s.disconnected {
		return nil
	}
	s.persistPending = true
	defer func() {
		if err == nil {
			s.persistPending = false
		}
	}()
	aead, err := githubSessionCipher(s.id)
	if err != nil {
		return err
	}
	var ws *githubWorkspace
	if s.Workspace != nil {
		copy := *s.Workspace
		ws = &copy
	}
	b, err := json.Marshal(githubSavedSession{Token: s.Token, RefreshToken: s.RefreshToken, Username: s.Username, Expires: s.Expires, TokenExpires: s.TokenExpires, RefreshExpires: s.RefreshExpires, Workspace: ws, Running: s.Running, Invalid: s.Invalid, AgentError: s.AgentError})
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	b = aead.Seal(nonce, nonce, b, []byte(s.binding))
	path := githubSessionPath(s.id)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".session-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	_, writeErr := f.Write(b)
	err = errors.Join(writeErr, f.Sync(), f.Close())
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func loadGitHubSession(id string) (*githubSession, error) {
	githubSessionLoadMu.Lock()
	defer githubSessionLoadMu.Unlock()
	if value, ok := githubSessions.Load(id); ok {
		s := value.(*githubSession)
		if s.binding != "" && s.binding != githubSessionBinding() {
			return nil, errors.New("GitHub configuration changed")
		}
		return s, nil
	}
	aead, err := githubSessionCipher(id)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(githubSessionPath(id))
	if err != nil {
		return nil, err
	}
	if len(b) < aead.NonceSize() {
		return nil, errors.New("invalid GitHub session")
	}
	binding := githubSessionBinding()
	b, err = aead.Open(nil, b[:aead.NonceSize()], b[aead.NonceSize():], []byte(binding))
	if err != nil {
		return nil, err
	}
	var saved githubSavedSession
	if err := json.Unmarshal(b, &saved); err != nil {
		return nil, err
	}
	if time.Now().After(saved.Expires) {
		return nil, errors.New("expired GitHub session")
	}
	if ws := saved.Workspace; ws != nil {
		root, err := filepath.Abs(githubWorkspaceRoot())
		if err != nil {
			return nil, err
		}
		path, err := filepath.Abs(ws.Path)
		if err != nil || filepath.Dir(path) != root || !githubRepoName.MatchString(ws.Repo) {
			return nil, errors.New("invalid GitHub workspace")
		}
		// A missing checkout must not discard an otherwise valid connection.
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("invalid GitHub workspace symlink")
		}
	}
	s := &githubSession{id: id, binding: binding, Token: saved.Token, RefreshToken: saved.RefreshToken, Username: saved.Username, Expires: saved.Expires, TokenExpires: saved.TokenExpires, RefreshExpires: saved.RefreshExpires, Workspace: saved.Workspace, Invalid: saved.Invalid, AgentError: saved.AgentError}
	if saved.Running {
		s.AgentError = "服务重启中断了任务，已有文件修改保留，请检查后重试。"
		if s.Workspace != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			s.Workspace.LastResult = map[string]any{"message": s.AgentError, "files": changedFiles(ctx, s.Workspace.Path), "diff": gitDiff(ctx, s.Workspace.Path)}
			cancel()
		}
	}
	githubSessions.Store(id, s)
	return s, nil
}

// refreshGitHubSessionLocked serializes token rotation with repository and push
// requests. GitHub invalidates the previous token pair when a refresh succeeds.
func refreshGitHubSessionLocked(ctx context.Context, s *githubSession) error {
	if s.Invalid || s.disconnected {
		return &githubHTTPError{Status: http.StatusUnauthorized}
	}
	// A successful remote rotation cannot be repeated with the old refresh
	// token. Retry its disk write before allowing subsequent remote requests.
	if s.persistPending {
		return saveGitHubSessionLocked(s)
	}
	if s.TokenExpires.IsZero() || time.Until(s.TokenExpires) > time.Minute {
		return nil
	}
	if s.RefreshToken == "" || (!s.RefreshExpires.IsZero() && time.Now().After(s.RefreshExpires)) {
		return &githubHTTPError{Status: http.StatusUnauthorized}
	}
	cfg := loadWebGitHubConfig()
	form := url.Values{"client_id": {cfg.ClientID}, "client_secret": {cfg.ClientSecret}, "grant_type": {"refresh_token"}, "refresh_token": {s.RefreshToken}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://github.com/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	var token struct {
		AccessToken           string `json:"access_token"`
		RefreshToken          string `json:"refresh_token"`
		ExpiresIn             int    `json:"expires_in"`
		RefreshTokenExpiresIn int    `json:"refresh_token_expires_in"`
		Error                 string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&token); err != nil {
		return errors.New("invalid GitHub refresh response")
	}
	if token.Error == "bad_refresh_token" {
		return &githubHTTPError{Status: http.StatusUnauthorized}
	}
	if res.StatusCode != http.StatusOK || token.Error != "" || token.AccessToken == "" || token.RefreshToken == "" || token.ExpiresIn <= 0 || token.RefreshTokenExpiresIn <= 0 {
		return errors.New("GitHub token refresh unavailable")
	}
	s.Token, s.RefreshToken = token.AccessToken, token.RefreshToken
	s.TokenExpires = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
	s.RefreshExpires = time.Now().Add(time.Duration(token.RefreshTokenExpiresIn) * time.Second)
	return saveGitHubSessionLocked(s)
}

func githubDisconnectAPI(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(githubSessionCookie)
	if err == nil {
		if _, err := githubSessionCipher(c.Value); err == nil {
			s, _ := loadGitHubSession(c.Value)
			if s != nil {
				s.mu.Lock()
				defer s.mu.Unlock()
			}
			if err := os.Remove(githubSessionPath(c.Value)); err != nil && !errors.Is(err, os.ErrNotExist) {
				http.Error(w, "无法断开 GitHub 连接，请重试", 500)
				return
			}
			if s != nil {
				s.disconnected, s.Invalid = true, true
				if s.Cancel != nil {
					s.Cancel()
				}
			}
			githubSessions.Delete(c.Value)
		}
	}
	secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	http.SetCookie(w, &http.Cookie{Name: githubSessionCookie, Path: "/", MaxAge: -1, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
	writeJSON(w, map[string]bool{"connected": false})
}

type githubHTTPError struct {
	Status int
	Detail string
}

func (e *githubHTTPError) Error() string {
	return fmt.Sprintf("GitHub returned HTTP %d: %s", e.Status, e.Detail)
}

// Call with s.mu held. A network outage or rate limit must not discard a grant.
func githubSessionError(w http.ResponseWriter, s *githubSession, err error, message string) {
	var response *githubHTTPError
	if errors.As(err, &response) && response.Status == http.StatusUnauthorized {
		s.Invalid = true
		if saveGitHubSessionLocked(s) != nil {
			http.Error(w, "无法保存 GitHub 连接状态，请重试", 500)
			return
		}
		http.Error(w, "GitHub 授权已失效，请重新连接；项目修改已保留", 401)
		return
	}
	http.Error(w, message, http.StatusBadGateway)
}
