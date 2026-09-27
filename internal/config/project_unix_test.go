//go:build unix

package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/tunahanaliozturk/derbent/internal/config"
)

// within runs fn and returns its error, failing the test if fn has not returned after a few seconds.
// Opening a FIFO for reading blocks until a writer opens it, so on a timeout it opens fifo for writing,
// which lets fn go on, and waits for fn before failing.
func within(t *testing.T, fifo string, fn func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		if w, err := os.OpenFile(fifo, os.O_WRONLY, 0); err == nil {
			w.Close()
		}
		<-done
		t.Fatal("it blocked on the FIFO")
		return nil
	}
}

// A FIFO committed as .derbent.toml, or put there by an agent, must not hold the hook until the CLI
// gives up on it, which would let the call run under the CLI's own permissions.
func TestProjectRulesRefuseAFIFO(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, config.ProjectRulesFile)
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("cannot create a FIFO here: %v", err)
	}
	err := within(t, fifo, func() error {
		_, err := config.NewProjectRules(dir).Load()
		return err
	})
	if err == nil || !strings.Contains(err.Error(), fifo) || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("err = %v, want it to name the file and say it is not a regular file", err)
	}
}

// A FIFO as .git is not read either: the directory is its own project.
func TestProjectRootDoesNotReadAFIFOGitFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wt")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, ".git")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("cannot create a FIFO here: %v", err)
	}
	var key string
	err := within(t, fifo, func() (err error) {
		key, err = config.ProjectKey(dir)
		return err
	})
	if err != nil || !strings.HasSuffix(key, "/wt") {
		t.Fatalf("key %q, err %v; want the directory itself", key, err)
	}
}
