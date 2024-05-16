package fs

import (
	"os"
	"strings"
)

// IsRegularFile returns true if fi belongs to a normal file. If fi is nil,
// false is returned.
func IsRegularFile(fi os.FileInfo) bool {
	if fi == nil {
		return false
	}

	return fi.Mode()&os.ModeType == 0
}

func IsPathIncluded(includes []string, path string) bool {
	var result bool = len(includes) == 0
	if !result {
		for _, x := range includes {
			if strings.Contains(x, path) || strings.Contains(path, x) {
				result = true
				break
			}
		}
	}
	return result
}

func IsPathRemoved(removes []string, path string) bool {
	if len(removes) != 0 {
		for _, x := range removes {
			if strings.Contains(path, x) {
				return true
			}
		}
	}
	return false
}
