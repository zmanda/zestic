//go:build aix
// +build aix

package restic

import (
	"os"
	"syscall"

	"github.com/restic/restic/internal/debug"
)

func (node Node) restoreSymlinkTimestamps(_ string, _ [2]syscall.Timespec) error {
	return nil
}

// AIX has a funny timespec type in syscall, with 32-bit nanoseconds.
// golang.org/x/sys/unix handles this cleanly, but we're stuck with syscall
// because os.Stat returns a syscall type in its os.FileInfo.Sys().
func toTimespec(t syscall.StTimespec_t) syscall.Timespec {
	return syscall.Timespec{Sec: t.Sec, Nsec: int64(t.Nsec)}
}

func (s statT) atim() syscall.Timespec { return toTimespec(s.Atim) }
func (s statT) mtim() syscall.Timespec { return toTimespec(s.Mtim) }
func (s statT) ctim() syscall.Timespec { return toTimespec(s.Ctim) }

// RestoreMetadata restores node metadata
func (node Node) RestoreMetadata(path string) (err error) {
	err = node.restoreMetadata(path)
	if err != nil {
		debug.Log("restoreMetadata(%s) error %v", path, err)
	}
	return err
}

// restoreExtendedAttributes is a no-op on AIX.
func (node Node) restoreExtendedAttributes(_ string) error {
	return nil
}

// fillExtendedAttributes is a no-op on AIX.
func (node *Node) fillExtendedAttributes(_ string) error {
	return nil
}

// restoreGenericAttributes is no-op on AIX.
func (node *Node) restoreGenericAttributes(_ string) error {
	return node.handleUnknownGenericAttributesFound()
}

// fillGenericAttributes is a no-op on AIX.
func (node *Node) fillGenericAttributes(_ string, _ os.FileInfo, _ *statT) (allowExtended bool, err error) {
	return true, nil
}
