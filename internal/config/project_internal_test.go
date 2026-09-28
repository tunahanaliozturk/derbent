package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readSmallFile refuses a file other than the one the check looked at, such as a symbolic link swapped
// in after the Lstat, whose target it would otherwise read and a parse error could echo.
func TestReadSmallFileRefusesAFileOtherThanTheOneChecked(t *testing.T) {
	dir := t.TempDir()
	checked, other := filepath.Join(dir, "checked.toml"), filepath.Join(dir, "other.toml")
	for _, p := range []string{checked, other} {
		if err := os.WriteFile(p, []byte("# "+filepath.Base(p)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fi, err := os.Lstat(checked)
	if err != nil {
		t.Fatal(err)
	}
	if data, rerr := readSmallFile(checked, fi); rerr != nil || string(data) != "# checked.toml\n" {
		t.Fatalf("the file that was checked: %q, %v", data, rerr)
	}
	if data, rerr := readSmallFile(other, fi); rerr == nil || !strings.Contains(rerr.Error(), "changed while it was being read") {
		t.Fatalf("another file: %q, err = %v; want it refused", data, rerr)
	}
}
