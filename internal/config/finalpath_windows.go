package config

import (
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

// normalizedDOSName asks GetFinalPathNameByHandle for FILE_NAME_NORMALIZED and VOLUME_NAME_DOS, which are
// both 0: the path in the case the directories hold it, on a drive letter where there is one.
const normalizedDOSName = 0

// finalPath is the path Windows gives the file or directory at path once it is open: symbolic links,
// junctions, subst drives and short names resolved, in the case the directories hold. It is the path git
// for Windows reports for a checkout. filepath.EvalSymlinks has followed no junction since Go 1.23.
func finalPath(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // a working directory or one above it, opened only to name it
	if err != nil {
		return "", err
	}
	defer f.Close()
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetFinalPathNameByHandle(windows.Handle(f.Fd()), &buf[0], windows.MAX_LONG_PATH, normalizedDOSName)
	if err != nil {
		return "", err
	}
	name := windows.UTF16ToString(buf[:min(int(n), len(buf))])
	if rest, ok := strings.CutPrefix(name, `\\?\UNC\`); ok {
		return `\\` + rest, nil
	}
	return strings.TrimPrefix(name, `\\?\`), nil
}
