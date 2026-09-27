package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/tunahanaliozturk/derbent/internal/rule"
)

// ProjectRoot returns the directory a project's key is made from: the root of the git repository
// containing dir, or dir itself outside a repository. A linked worktree belongs to the repository it was
// added from, so agents working in separate worktrees of one repository share a project. The root is
// absolute, cleaned, with symbolic links and Windows short names resolved, in the path's own case, so
// files at the root, such as the project rules file, can be found on a case-sensitive directory.
func ProjectRoot(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve project %s: %w", dir, err)
	}
	root := resolve(abs)
	for d := root; ; d = filepath.Dir(d) {
		if fi, statErr := os.Stat(filepath.Join(d, ".git")); statErr == nil {
			root = d
			if !fi.IsDir() {
				root = mainCheckout(d)
			}
			break
		}
		if filepath.Dir(d) == d {
			break
		}
	}
	return filepath.Clean(resolve(root)), nil
}

// ProjectKey returns the key that notes and receipts for dir are filed under: its ProjectRoot, written
// with forward slashes and lower-cased on Windows, so that one project reached through different
// spellings has one key.
func ProjectKey(dir string) (string, error) {
	root, err := ProjectRoot(dir)
	if err != nil {
		return "", err
	}
	key := filepath.ToSlash(root)
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	return key, nil
}

// ProjectRulesFile is the file at a project's root whose rules can only tighten the user's (ADR 0014).
const ProjectRulesFile = ".derbent.toml"

// ParseProjectRules decodes a project's rules file, which may hold [[rule]] tables and nothing else.
// name only appears in error messages.
func ParseProjectRules(name, text string) (rule.Set, error) {
	var f struct {
		Rules []rule.Spec `toml:"rule"`
	}
	md, err := toml.Decode(text, &f)
	if err != nil {
		return rule.Set{}, fmt.Errorf("project rules %s: %w", name, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		return rule.Set{}, fmt.Errorf("project rules %s: only [[rule]] tables are allowed, found %s", name, strings.Join(keys, ", "))
	}
	set, err := rule.CompileProject(f.Rules)
	if err != nil {
		return rule.Set{}, fmt.Errorf("project rules %s: %w", name, err)
	}
	return set, nil
}

// ProjectRules reads one project's rules file for every call, and keeps what it read until the file's
// size or modification time changes, so an edit takes effect on the next call without reading an
// unchanged file again. An edit that keeps both, which only a file system with a coarse clock allows
// within one tick, is seen with the next change. It is safe for concurrent use.
type ProjectRules struct {
	path string

	mu   sync.Mutex
	read bool // set and err hold what the file said at size and mod
	size int64
	mod  time.Time
	set  rule.Set
	err  error
}

// NewProjectRules returns the rules file at root, a directory from ProjectRoot.
func NewProjectRules(root string) *ProjectRules {
	return &ProjectRules{path: filepath.Join(root, ProjectRulesFile)}
}

// Load returns the project's rules. A missing file gives an empty set, which adds nothing to any call.
// A file that cannot be read or is invalid gives an error that names it: the project's rules cannot be
// known, so the caller refuses the call.
func (p *ProjectRules) Load() (rule.Set, error) {
	fi, err := os.Stat(p.path)
	if errors.Is(err, fs.ErrNotExist) {
		return rule.Set{}, nil
	}
	if err != nil {
		return rule.Set{}, fmt.Errorf("project rules %s: %w; every call in this project is refused until it can be read", p.path, err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.read && fi.Size() == p.size && fi.ModTime().Equal(p.mod) {
		return p.set, p.err
	}
	data, err := os.ReadFile(p.path) //nolint:gosec // the rules file at the root of the project being worked on
	if err != nil {
		return rule.Set{}, fmt.Errorf("project rules %s: %w; every call in this project is refused until it can be read", p.path, err)
	}
	set, perr := ParseProjectRules(p.path, string(data))
	if perr != nil {
		perr = fmt.Errorf("%w; every call in this project is refused until the file is fixed or removed", perr)
	}
	p.read, p.size, p.mod, p.set, p.err = true, fi.Size(), fi.ModTime(), set, perr
	return set, perr
}

// mainCheckout maps a directory whose .git is a file to the repository it belongs to. A linked
// worktree's .git file points at <repo>/.git/worktrees/<name>, which holds a commondir file leading
// back to <repo>/.git. A submodule's .git file points at a git directory without commondir, so the
// submodule stays a project of its own, as does anything whose .git file cannot be read.
func mainCheckout(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, ".git")) //nolint:gosec // reading the .git file of the user's own checkout
	if err != nil {
		return dir
	}
	gitDir, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
	if !ok {
		return dir
	}
	gitDir = strings.TrimSpace(gitDir)
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(dir, gitDir)
	}
	common, err := os.ReadFile(filepath.Join(gitDir, "commondir")) //nolint:gosec // a file inside the same checkout's git directory
	if err != nil {
		return dir
	}
	commonDir := strings.TrimSpace(string(common))
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(gitDir, commonDir)
	}
	commonDir = filepath.Clean(commonDir)
	if filepath.Base(commonDir) == ".git" {
		return filepath.Dir(commonDir)
	}
	return commonDir // a worktree of a bare repository
}

// resolve expands symbolic links and Windows short names, and keeps the path as it is when that fails.
func resolve(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}
