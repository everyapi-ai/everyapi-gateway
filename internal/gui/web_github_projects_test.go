package gui

import (
	"context"
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
