package restorer

import "github.com/restic/restic/internal/restic"

// addFile adds the file to restorer's progress tracker.
// If the node represents an ads file, it only adds the size without counting the ads file.
func (res *Restorer) addFile(node *restic.Node, size uint64) {
	if node.IsMainFile() {
		res.progress.AddFile(size)
	} else {
		// If this is not the main file, we just want to update the size and not the count.
		res.progress.AddSize(size)
	}
}
