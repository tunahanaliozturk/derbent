package config_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tunahanaliozturk/derbent/internal/config"
	"github.com/tunahanaliozturk/derbent/internal/rule"
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

// Agents often work in linked worktrees of one repository. They must share one project key, or a note
// written in one worktree is invisible from the others.
func TestProjectKeyJoinsLinkedWorktrees(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	base := t.TempDir()
	repo := filepath.Join(base, "shop")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), git, append([]string{"-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	sibling := filepath.Join(base, "shop-feature")
	nested := filepath.Join(repo, ".claude", "worktrees", "agent1")
	runGit("init", "-q")
	runGit("commit", "-q", "--allow-empty", "-m", "start")
	runGit("worktree", "add", "-q", sibling)
	runGit("worktree", "add", "-q", nested)

	want := mustKey(t, repo)
	for name, dir := range map[string]string{"sibling worktree": sibling, "worktree inside the repository": nested} {
		if got := mustKey(t, dir); got != want {
			t.Errorf("%s: key %q, want %q", name, got, want)
		}
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

func TestParseProjectRulesTakesRulesAndNothingElse(t *testing.T) {
	set, err := config.ParseProjectRules("p.toml", "[[rule]]\ntool = \"native__Bash\"\nargs = { command = \"git push*\" }\naction = \"ask\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if d := set.Decide("claude", "native__Bash", map[string]any{"command": "git push"}); d.Rule != 1 || d.Action != rule.Ask {
		t.Fatalf("git push = %+v", d)
	}
	if d := set.Decide("claude", "native__Bash", map[string]any{"command": "ls"}); d.Rule != 0 {
		t.Fatalf("ls = %+v, want no match: project rules need no catch-all", d)
	}
	if _, err = config.ParseProjectRules("p.toml", ""); err != nil {
		t.Fatalf("an empty file: %v", err)
	}
	for name, text := range map[string]string{
		"a server":       "[servers.x]\ncommand = [\"x\"]\n",
		"approvals":      "[approvals]\ntimeout = \"1s\"\n",
		"a budget":       "[[budget]]\ncalls = 1\nper = \"1h\"\n",
		"a misspelt key": "[[rule]]\nacton = \"deny\"\n",
		"a bad action":   "[[rule]]\naction = \"maybe\"\n",
		"a bad agent":    "[[rule]]\nagent = \"Claude\"\naction = \"deny\"\n",
		"bad syntax":     "[[rule]\n",
	} {
		if _, perr := config.ParseProjectRules("p.toml", text); perr == nil || !strings.Contains(perr.Error(), "p.toml") {
			t.Errorf("%s: err = %v, want an error naming the file", name, perr)
		}
	}
}

// Notepad and other Windows editors can save UTF-8 with a byte order mark and CRLF line endings.
func TestProjectRulesFromAWindowsEditorParse(t *testing.T) {
	set, err := config.ParseProjectRules("p.toml", "\ufeff[[rule]]\r\ntool = \"native__Bash\"\r\naction = \"deny\"\r\n")
	if err != nil {
		t.Fatal(err)
	}
	if d := set.Decide("claude", "native__Bash", nil); d.Rule != 1 || d.Action != rule.Deny {
		t.Fatalf("decision = %+v", d)
	}
}

// The file is read again when its size or modification time changes, and not otherwise.
func TestProjectRulesReadTheFileAgainOnlyWhenItChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, config.ProjectRulesFile)
	write := func(tool string, mod time.Time) {
		t.Helper()
		if err := os.WriteFile(path, []byte("[[rule]]\ntool = \""+tool+"\"\naction = \"deny\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	matches := func(p *config.ProjectRules, tool string) bool {
		t.Helper()
		set, err := p.Load()
		if err != nil {
			t.Fatal(err)
		}
		return set.Decide("claude", tool, nil).Rule == 1
	}
	p := config.NewProjectRules(dir)
	if matches(p, "aaaa") {
		t.Fatal("a missing file matched")
	}
	then := time.Now().Add(-time.Hour).Truncate(time.Second)
	write("aaaa", then)
	if !matches(p, "aaaa") {
		t.Fatal("the new file was not read")
	}
	write("bbbb", then) // same size, same modification time
	if !matches(p, "aaaa") {
		t.Fatal("an unchanged size and time read the file again")
	}
	write("bbbb", then.Add(time.Second))
	if !matches(p, "bbbb") || matches(p, "aaaa") {
		t.Fatal("a new modification time did not read the file again")
	}
	if err := os.WriteFile(path, []byte("[servers.x]\ncommand = [\"x\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Load(); err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "every call in this project is refused") {
		t.Fatalf("an invalid file: err = %v", err)
	}
}

// On Windows the key is lower-cased, but a directory can be case-sensitive there, so the rules file is
// looked up under the root in the path's own case.
func TestProjectRootKeepsTheCaseOfThePath(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "MyRepo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(repo, "Sub")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := config.ProjectRoot(sub)
	if err != nil || filepath.Base(root) != "MyRepo" {
		t.Fatalf("ProjectRoot = %q, %v; want the repository root in its own case", root, err)
	}
	key, err := config.ProjectKey(sub)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.ToSlash(root)
	if runtime.GOOS == "windows" {
		want = strings.ToLower(want)
	}
	if key != want {
		t.Fatalf("ProjectKey = %q, want %q", key, want)
	}
}
