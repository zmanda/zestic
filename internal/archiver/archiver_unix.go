//go:build !windows
// +build !windows

package archiver

import (
	"context"
	"os"
	"path/filepath"
	"sort"

	"github.com/restic/restic/internal/debug"
	"github.com/restic/restic/internal/fs"
	"github.com/restic/restic/internal/restic"
)

// preProcessTargets performs preprocessing of the targets before the loop.
// It is a no-op on linux as we do not need to do an
// extra iteration on the targets before the loop.
// We process each target inside the loop.
func preProcessTargets(filesys fs.FS, targets *[]string) {
	// no-op
}

// processTarget process each target in the loop.
func processTarget(target string) string {
	return filesys.Clean(target)
}

// SaveDir stores a directory in the repo and returns the node. snPath is the
// path within the current snapshot.
func (arch *Archiver) SaveDir(ctx context.Context, snPath string, dir string, fi os.FileInfo, previous *restic.Tree, complete CompleteFunc) (d FutureNode, err error) {
	debug.Log("%v %v", snPath, dir)

	treeNode, err := arch.nodeFromFileInfo(snPath, dir, fi)
	if err != nil {
		return FutureNode{}, err
	}

	names, err := readdirnames(arch.FS, dir, fs.O_NOFOLLOW)
	if err != nil {
		return FutureNode{}, err
	}

	sort.Strings(names)

	nodes := make([]FutureNode, 0, len(names))

	for _, name := range names {
		// test if context has been cancelled
		if ctx.Err() != nil {
			debug.Log("context has been cancelled, aborting")
			return FutureNode{}, ctx.Err()
		}

		pathname := arch.FS.Join(dir, name)

		name := filepath.Base(pathname)
		oldNode := previous.Find(name)
		snItem := join(snPath, name)
		fn, excluded, err := arch.Save(ctx, snItem, pathname, oldNode)

		// return error early if possible
		if err != nil {
			err = arch.error(pathname, err)
			if err == nil {
				// ignore error
				continue
			}

			return FutureNode{}, err
		}

		if excluded {
			continue
		}

		nodes = append(nodes, fn)
	}

	fn := arch.treeSaver.Save(ctx, snPath, dir, treeNode, nodes, complete)

	return fn, nil
}

// processTargets is no-op for non-windows OS
func (arch *Archiver) processTargets(_ string, _ string, _ string, fiMain os.FileInfo) (fi os.FileInfo, shouldReturn bool, fn FutureNode, excluded bool, err error) {
	return fiMain, false, FutureNode{}, false, nil
}
