package gui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
