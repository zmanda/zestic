//go:build !windows
// +build !windows

package restore

import "github.com/restic/restic/internal/restic"

// incrementFilesFinished increments the files finished count
func (p *Progress) incrementFilesFinished(_ *restic.Node) {
	p.filesFinished++
}
