package backup

import "github.com/restic/restic/internal/restic"

// incrementNewFiles increments the new files count if it is a main file
func (p *Progress) incrementNewFiles(node *restic.Node) {
	if node.IsMainFile() {
		p.summary.Files.New++
	}
}

// incrementNewFiles increments the unchanged files count if it is a main file
func (p *Progress) incrementUnchangedFiles(node *restic.Node) {
	if node.IsMainFile() {
		p.summary.Files.Unchanged++
	}
}

// incrementNewFiles increments the changed files count if it is a main file
func (p *Progress) incrementChangedFiles(node *restic.Node) {
	if node.IsMainFile() {
		p.summary.Files.Changed++
	}
}
