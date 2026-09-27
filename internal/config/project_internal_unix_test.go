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
	fifo := filepath.Join(t.TempDir(), ProjectRulesFile)
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("cannot create a FIFO here: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := readSmallFile(fifo)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("err = %v, want it to say the file is not a regular file", err)
		}
	case <-time.After(5 * time.Second):
		if w, err := os.OpenFile(fifo, os.O_WRONLY, 0); err == nil {
			w.Close()
		}
		<-done
		t.Fatal("readSmallFile blocked on the FIFO")
	}
}
