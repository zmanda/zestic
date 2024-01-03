package restic

import (
	"os"
	"syscall"
)

func (node Node) restoreSymlinkTimestamps(path string, utimes [2]syscall.Timespec) error {
	return nil
}

func (s statT) atim() syscall.Timespec { return s.Atim }
func (s statT) mtim() syscall.Timespec { return s.Mtim }
func (s statT) ctim() syscall.Timespec { return s.Ctim }

// restoreExtendedAttributes is a no-op on openbsd.
func (node Node) restoreExtendedAttributes(path string) error {
	return nil
}

// fillExtendedAttributes is a no-op on openbsd.
func (node *Node) fillExtendedAttributes(path string) error {
	return nil
}

// restoreGenericAttributes is no-op on openbsd.
func (node *Node) restoreGenericAttributes(path string) error {
	for _, attr := range node.GenericAttributes {
		handleUnknownGenericAttributeFound(attr.Name)
	}
	return nil
}

// fillGenericAttributes is a no-op on openbsd.
func (node *Node) fillGenericAttributes(path string, fi os.FileInfo, stat *statT) error {
	return nil
}
