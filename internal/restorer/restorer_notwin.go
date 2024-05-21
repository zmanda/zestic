//go:build !windows
// +build !windows

package restorer

import "github.com/restic/restic/internal/restic"

// addFile adds the file to restorer's progress tracker
func (res *Restorer) addFile(_ *restic.Node, size uint64) {
	res.progress.AddFile(size)
}
