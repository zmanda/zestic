package restic

import (
	"os"
	"syscall"

	"github.com/restic/restic/internal/debug"
)

func (node Node) restoreSymlinkTimestamps(_ string, _ [2]syscall.Timespec) error {
	return nil
}

func (s statT) atim() syscall.Timespec { return s.Atimespec }
func (s statT) mtim() syscall.Timespec { return s.Mtimespec }
func (s statT) ctim() syscall.Timespec { return s.Ctimespec }

// RestoreMetadata restores node metadata
func (node Node) RestoreMetadata(path string) (err error) {
	err = node.restoreMetadata(path)
	if err != nil {
		debug.Log("restoreMetadata(%s) error %v", path, err)
	}
	return err
}

// restoreExtendedAttributes is a no-op on netbsd.
func (node Node) restoreExtendedAttributes(_ string) error {
	return nil
}

// fillExtendedAttributes is a no-op on netbsd.
func (node *Node) fillExtendedAttributes(_ string) error {
	return nil
}

// restoreGenericAttributes is no-op on netbsd.
func (node *Node) restoreGenericAttributes(_ string) error {
	return node.handleUnknownGenericAttributesFound()
}

// fillGenericAttributes is a no-op on netbsd.
func (node *Node) fillGenericAttributes(_ string, _ os.FileInfo, _ *statT) (allowExtended bool, err error) {
	return true, nil
}
