package config

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/tunahanaliozturk/derbent/internal/rule"
)

// CheckoutRoot returns the root of the checkout that contains dir: the nearest directory at or above
// dir that holds .git, which for a linked worktree is the worktree's own root, or dir itself outside a
// repository. The project rules file is read there (ADR 0014). The root is absolute, cleaned, with
// symbolic links and Windows short names resolved, in the path's own case, so the rules file can be
// found on a case-sensitive directory.
func CheckoutRoot(dir string) (string, error) {
	root, _, err := checkoutRoot(dir)
	return root, err
}

// ProjectRoot returns the directory a project's key is made from: the CheckoutRoot of dir, except that a
// linked worktree belongs to the repository it was added from, so agents working in separate worktrees
// of one repository share a project.
func ProjectRoot(dir string) (string, error) {
	root, gitFile, err := checkoutRoot(dir)
	if err != nil || !gitFile {
		return root, err
	}
	return filepath.Clean(resolve(mainCheckout(root))), nil
}

// checkoutRoot finds the CheckoutRoot of dir and reports whether its .git is a file, as in a linked
// worktree or a submodule, rather than a directory.
func checkoutRoot(dir string) (root string, gitFile bool, err error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", false, fmt.Errorf("resolve project %s: %w", dir, err)
	}
	root = resolve(abs)
	for d := root; ; d = filepath.Dir(d) {
		if fi, statErr := os.Stat(filepath.Join(d, ".git")); statErr == nil {
			return filepath.Clean(d), !fi.IsDir(), nil
		}
		if filepath.Dir(d) == d {
			return filepath.Clean(root), false, nil
		}
	}
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

// ProjectRulesFile is the file at a checkout's root whose rules can only tighten the user's (ADR 0014).
const ProjectRulesFile = ".derbent.toml"

// maxSmallFile is the most a project rules file or a .git file may hold. Either is read on every call,
// from a directory an agent may control.
const maxSmallFile = 64 << 10

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

// NewProjectRules returns the rules file at root, a directory from CheckoutRoot.
func NewProjectRules(root string) *ProjectRules {
	return &ProjectRules{path: filepath.Join(root, ProjectRulesFile)}
}

// Load returns the project's rules. A missing file gives an empty set, which adds nothing to any call.
// A file that cannot be read, is not a regular file of at most 64 KiB, or is invalid gives an error that
// names it: the project's rules cannot be known, so the caller refuses the call.
func (p *ProjectRules) Load() (rule.Set, error) {
	fi, err := os.Lstat(p.path)
	if errors.Is(err, fs.ErrNotExist) {
		return rule.Set{}, nil
	}
	if err != nil {
		return rule.Set{}, fmt.Errorf("project rules %s: %w; every call in this project is refused until it can be read", p.path, err)
	}
	if err = checkSmallFile(fi); err != nil {
		return rule.Set{}, fmt.Errorf("project rules %s: %w; every call in this project is refused until the file is fixed or removed", p.path, err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.read && fi.Size() == p.size && fi.ModTime().Equal(p.mod) {
		return p.set, p.err
	}
	data, err := readSmallFile(p.path)
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
	data, err := lstatAndReadSmallFile(filepath.Join(dir, ".git"))
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
	common, err := lstatAndReadSmallFile(filepath.Join(gitDir, "commondir"))
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

// checkSmallFile refuses what os.Lstat reported unless it is a regular file of at most maxSmallFile
// bytes. A FIFO or a device could block the read or never end it, and a symbolic link could lead to
// either, or to a file whose start a parse error would echo.
func checkSmallFile(fi fs.FileInfo) error {
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		return errors.New("it is a symbolic link, and only a regular file is read")
	case !fi.Mode().IsRegular():
		return errors.New("it is not a regular file")
	case fi.Size() > maxSmallFile:
		return fmt.Errorf("it is larger than %d KiB", maxSmallFile>>10)
	}
	return nil
}

// readSmallFile reads a file that checkSmallFile accepted. A file replaced since the check is refused
// unless it is still a regular file: the open does not wait for a FIFO's writer (O_NONBLOCK, which
// Windows ignores), and the type is checked again on what was opened. It reads at most one byte past
// the limit, so a file that grew since the check is refused rather than read whole.
func readSmallFile(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0) //nolint:gosec // a rules or .git file that checkSmallFile accepted
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("it is not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxSmallFile+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSmallFile {
		return nil, fmt.Errorf("it is larger than %d KiB", maxSmallFile>>10)
	}
	return data, nil
}

// lstatAndReadSmallFile reads path if it is a regular file of at most maxSmallFile bytes.
func lstatAndReadSmallFile(path string) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if err = checkSmallFile(fi); err != nil {
		return nil, err
	}
	return readSmallFile(path)
}

// resolve expands symbolic links and Windows short names, and keeps the path as it is when that fails.
func resolve(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}
