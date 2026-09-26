package config_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tunahanaliozturk/portcullis/internal/config"
)

func mustKey(t *testing.T, dir string) string {
	t.Helper()
	key, err := config.ProjectKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestProjectKeyIsTheRepositoryRoot(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "shop")
	sub := filepath.Join(repo, "internal", "billing")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	root := mustKey(t, repo)
	for name, dir := range map[string]string{
		"subdirectory":       sub,
		"trailing separator": repo + string(filepath.Separator),
		"dot segments":       filepath.Join(sub, "..", ".."),
	} {
		if got := mustKey(t, dir); got != root {
			t.Errorf("%s: key %q, want %q", name, got, root)
		}
	}
	if strings.Contains(root, `\`) {
		t.Errorf("key %q has backslashes", root)
	}
}

func TestProjectKeyAcceptsWorktreeGitFile(t *testing.T) {
	wt := filepath.Join(t.TempDir(), "wt")
	if err := os.MkdirAll(filepath.Join(wt, "cmd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: /elsewhere\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, want := mustKey(t, filepath.Join(wt, "cmd")), mustKey(t, wt); got != want {
		t.Fatalf("key %q, want %q", got, want)
	}
}

func TestProjectKeyOutsideRepositoryIsTheDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "notes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(mustKey(t, dir), "/notes") {
		t.Fatalf("key %q does not end with the directory", mustKey(t, dir))
	}
}

func TestProjectKeyIgnoresCaseOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("paths are case-sensitive here")
	}
	dir := t.TempDir()
	if got, want := mustKey(t, strings.ToUpper(dir)), mustKey(t, dir); got != want {
		t.Fatalf("key %q, want %q", got, want)
	}
}
