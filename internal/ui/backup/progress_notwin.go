//go:build !windows
// +build !windows

package backup

import "github.com/restic/restic/internal/restic"

// incrementNewFiles increments the new files count
func (p *Progress) incrementNewFiles(_ *restic.Node) {
	p.summary.Files.New++
}

// incrementNewFiles increments the unchanged files count
func (p *Progress) incrementUnchangedFiles(_ *restic.Node) {
	p.summary.Files.Unchanged++
}

// incrementNewFiles increments the changed files count
func (p *Progress) incrementChangedFiles(_ *restic.Node) {
	p.summary.Files.Changed++
}
