package gui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	githubStartPath    = "/_github/start"
	githubCallbackPath = "/_github/callback"
	githubStateCookie  = "magpie_github_state"
	githubStateAge     = 10 * time.Minute
)

type githubAuthConfig struct {
	ClientID     string
	ClientSecret string
	Organization string
}

func loadWebGitHubConfig() githubAuthConfig {
	secret := strings.TrimSpace(os.Getenv("EVERYAPI_GITHUB_CLIENT_SECRET"))
	if secret == "" {
		if path := strings.TrimSpace(os.Getenv("EVERYAPI_GITHUB_CLIENT_SECRET_FILE")); path != "" {
			if b, err := os.ReadFile(path); err == nil {
				secret = strings.TrimSpace(string(b))
			}
		}
	}
	return githubAuthConfig{
		ClientID:     strings.TrimSpace(os.Getenv("EVERYAPI_GITHUB_CLIENT_ID")),
		ClientSecret: secret,
		Organization: strings.TrimSpace(os.Getenv("EVERYAPI_GITHUB_ORG")),
	}
}

func (c githubAuthConfig) enabled() bool {
	return c.ClientID != "" && c.ClientSecret != "" && c.Organization != ""
}

// webGitHubRoutes exposes only the OAuth handshake without weakening the
// normal web guard. Once GitHub has verified organization membership, the
// callback mints the same short-lived web session cookie as the password flow.
func webGitHubRoutes(cookie, key string, keep time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != githubStartPath && r.URL.Path != githubCallbackPath {
			next.ServeHTTP(w, r)
			return
		}
		cfg := loadWebGitHubConfig()
		if !cfg.enabled() {
			http.Error(w, "GitHub 组织登录尚未配置", http.StatusNotFound)
			return
		}
		switch r.URL.Path {
		case githubStartPath:
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			state, err := randomWebGitHubState()
			if err != nil {
				http.Error(w, "无法创建 GitHub 登录会话", http.StatusInternalServerError)
				return
			}
			secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
			http.SetCookie(w, &http.Cookie{Name: githubStateCookie, Value: state, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: int(githubStateAge / time.Second)})
			redirect := githubCallbackURL(r)
			q := url.Values{"client_id": {cfg.ClientID}, "redirect_uri": {redirect}, "scope": {"read:org user:email"}, "state": {state}}
			http.Redirect(w, r, "https://github.com/login/oauth/authorize?"+q.Encode(), http.StatusFound)
		case githubCallbackPath:
			webGitHubCallback(w, r, cfg, cookie, key, keep)
		}
	})
}

func randomWebGitHubState() (string, error) {
	b := make([]byte, 24)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func githubCallbackURL(r *http.Request) string {
	if public := strings.TrimRight(os.Getenv("EVERYAPI_PUBLIC_URL"), "/"); public != "" {
		return public + githubCallbackPath
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host + githubCallbackPath
}

func webGitHubCallback(w http.ResponseWriter, r *http.Request, cfg githubAuthConfig, cookie, key string, keep time.Duration) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	stateCookie, err := r.Cookie(githubStateCookie)
	validState := err == nil && stateCookie.Value != "" && r.URL.Query().Get("state") != "" && subtle.ConstantTimeCompare([]byte(stateCookie.Value), []byte(r.URL.Query().Get("state"))) == 1
	clearGitHubStateCookie(w, r)
	if !validState {
		webGitHubError(w, "GitHub 登录状态已失效，请重新开始登录。")
		return
	}
	if oauthErr := r.URL.Query().Get("error"); oauthErr != "" {
		webGitHubError(w, "GitHub 登录已取消。")
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" {
		webGitHubError(w, "GitHub 没有返回登录授权。")
		return
	}
	ctx := r.Context()
	client := &http.Client{Timeout: 15 * time.Second}
	form := url.Values{"client_id": {cfg.ClientID}, "client_secret": {cfg.ClientSecret}, "code": {code}, "redirect_uri": {githubCallbackURL(r)}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://github.com/login/oauth/access_token", strings.NewReader(form.Encode()))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := client.Do(req)
	if err != nil {
		webGitHubError(w, "GitHub 登录服务暂时不可用，请稍后重试。")
		return
	}
	defer res.Body.Close()
	var token struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&token) != nil || token.AccessToken == "" {
		webGitHubError(w, "GitHub 授权交换失败，请稍后重试。")
		return
	}
	username, err := githubUsername(ctx, client, token.AccessToken)
	if err != nil || !githubOrgMember(ctx, client, token.AccessToken, cfg.Organization, username) {
		webGitHubError(w, "此 GitHub 账号不属于允许的组织。")
		return
	}
	secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	maxAge := int(keep / time.Second)
	http.SetCookie(w, &http.Cookie{Name: cookie, Value: key, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func githubUsername(ctx context.Context, client *http.Client, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		return "", err
	}
	githubHeaders(req, token)
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github user: %s", res.Status)
	}
	var user struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&user); err != nil || strings.TrimSpace(user.Login) == "" {
		return "", fmt.Errorf("github user response invalid")
	}
	return user.Login, nil
}

func githubOrgMember(ctx context.Context, client *http.Client, token, org, username string) bool {
	endpoint := "https://api.github.com/orgs/" + url.PathEscape(org) + "/members/" + url.PathEscape(username)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false
	}
	githubHeaders(req, token)
	res, err := client.Do(req)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	return res.StatusCode == http.StatusNoContent || res.StatusCode == http.StatusOK
}

func githubHeaders(req *http.Request, token string) {
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
}

func clearGitHubStateCookie(w http.ResponseWriter, r *http.Request) {
	secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	http.SetCookie(w, &http.Cookie{Name: githubStateCookie, Value: "", Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

func webGitHubError(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusUnauthorized)
	fmt.Fprintf(w, `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>GitHub 登录</title><style>body{font:16px/1.6 system-ui,sans-serif;background:#f5f6f8;color:#202124;margin:0;padding:16vh 20px;text-align:center}main{max-width:420px;margin:auto;padding:28px;background:#fff;border:1px solid #e1e4e8;border-radius:16px}a{display:inline-block;margin-top:16px;color:#1e5eff}</style><main><h1>GitHub 登录失败</h1><p>%s</p><a href="/">返回登录</a></main></html>`, html.EscapeString(message))
}
