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

// resolveRelativeTargets replaces targets that only contain relative
// directories ("." or "../../") with the contents of the directory. Each
// element of target is processed with fs.Clean().
func resolveRelativeTargets(filesys fs.FS, targets []string) ([]string, error) {
	debug.Log("targets before resolving: %v", targets)
	result := make([]string, 0, len(targets))
	for _, target := range targets {
		target = filesys.Clean(target)
		pc, _ := pathComponents(filesys, target, false)
		if len(pc) > 0 {
			result = append(result, target)
			continue
		}

		debug.Log("replacing %q with readdir(%q)", target, target)
		entries, err := readdirnames(filesys, target, fs.O_NOFOLLOW)
		if err != nil {
			return nil, err
		}
		sort.Strings(entries)

		for _, name := range entries {
			result = append(result, filesys.Join(target, name))
		}
	}

	debug.Log("targets after resolving: %v", result)
	return result, nil
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
