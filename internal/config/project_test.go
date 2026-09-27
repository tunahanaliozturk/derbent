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
// looked up under the checkout's root in the path's own case.
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
	if checkout, cerr := config.CheckoutRoot(sub); cerr != nil || checkout != root {
		t.Fatalf("CheckoutRoot = %q, %v; want %q", checkout, cerr, root)
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

// loadRefusal loads the rules file in dir and returns the error, failing the test unless it names the
// file, says why with want, and says every call is refused.
func loadRefusal(t *testing.T, dir, want string) {
	t.Helper()
	_, err := config.NewProjectRules(dir).Load()
	path := filepath.Join(dir, config.ProjectRulesFile)
	if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), want) ||
		!strings.Contains(err.Error(), "every call in this project is refused") {
		t.Fatalf("err = %v, want it to name %s, say %q and refuse every call", err, path, want)
	}
}

// Only a regular file of at most 64 KiB is read as a project's rules. Anything else could block the hook
// or fill its memory, and a symbolic link could echo the start of the file it points at in a parse error,
// so it is refused, which denies every call in the project.
func TestProjectRulesReadOnlyASmallRegularFile(t *testing.T) {
	rules := "[[rule]]\ntool = \"native__Bash\"\naction = \"deny\"\n"
	t.Run("a directory", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, config.ProjectRulesFile), 0o700); err != nil {
			t.Fatal(err)
		}
		loadRefusal(t, dir, "not a regular file")
	})
	t.Run("64 KiB", func(t *testing.T) {
		dir := t.TempDir()
		text := rules + "#" + strings.Repeat("x", 64<<10-len(rules)-2) + "\n"
		if err := os.WriteFile(filepath.Join(dir, config.ProjectRulesFile), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		set, err := config.NewProjectRules(dir).Load()
		if err != nil || set.Decide("claude", "native__Bash", nil).Rule != 1 {
			t.Fatalf("a file of exactly 64 KiB: %v", err)
		}
	})
	t.Run("larger than 64 KiB", func(t *testing.T) {
		dir := t.TempDir()
		text := rules + "#" + strings.Repeat("x", 64<<10) + "\n"
		if err := os.WriteFile(filepath.Join(dir, config.ProjectRulesFile), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		loadRefusal(t, dir, "larger than 64 KiB")
	})
	t.Run("a symbolic link", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "rules.toml")
		if err := os.WriteFile(target, []byte(rules), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, config.ProjectRulesFile)); err != nil {
			t.Skipf("cannot create a symbolic link here: %v", err)
		}
		loadRefusal(t, dir, "symbolic link")
	})
}

// A .git file is read only when it is a small regular file. A larger one is taken as not a linked
// worktree, so the directory is its own project, even when its first line points at a real worktree.
func TestProjectRootReadsOnlyASmallGitFile(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "shop")
	gitDir := filepath.Join(repo, ".git", "worktrees", "wt")
	if err := os.MkdirAll(gitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "commondir"), []byte("../..\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(base, "wt")
	if err := os.Mkdir(wt, 0o700); err != nil {
		t.Fatal(err)
	}
	link := "gitdir: " + gitDir + "\n"
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte(link), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, want := mustKey(t, wt), mustKey(t, repo); got != want {
		t.Fatalf("a small .git file: key %q, want the main checkout's %q", got, want)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte(link+strings.Repeat("\n", 64<<10)), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := mustKey(t, wt); !strings.HasSuffix(got, "/wt") {
		t.Fatalf("a .git file over 64 KiB: key %q, want the directory itself", got)
	}
}
