//go:build unix

package config

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A FIFO put in place of the rules file after checkSmallFile passed must not block the open, or the
// hook would wait until the CLI gave up on it and ran the call under its own permissions.
func TestReadSmallFileRefusesAFIFOWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	regular := filepath.Join(dir, "checked.toml")
	if err := os.WriteFile(regular, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	checked, err := os.Lstat(regular) // what the check saw before the FIFO took its place
	if err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, ProjectRulesFile)
	if err = syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("cannot create a FIFO here: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, rerr := readSmallFile(fifo, checked)
		done <- rerr
	}()
	select {
	case err = <-done:
		if err == nil || !strings.Contains(err.Error(), "changed while it was being read") {
			t.Fatalf("err = %v, want it refused", err)
		}
	case <-time.After(5 * time.Second):
		if w, werr := os.OpenFile(fifo, os.O_WRONLY, 0); werr == nil {
			w.Close()
		}
		<-done
		t.Fatal("readSmallFile blocked on the FIFO")
	}
}
