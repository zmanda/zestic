package archiver

import (
	"context"
	"os"
	"path/filepath"
	"sort"

	"github.com/restic/restic/internal/debug"
	"github.com/restic/restic/internal/errors"
	"github.com/restic/restic/internal/fs"
	"github.com/restic/restic/internal/restic"
)

// preProcessTargets performs preprocessing of the targets before the loop.
// For Windows, it cleans each target and it also adds ads stream for each
// target to the targets array.
// We read the ADS from each file and add them as independent Nodes with
// the full ADS name as the name of the file.
// During restore the ADS files are restored using the ADS name and that
// automatically attaches them as ADS to the main file.
func preProcessTargets(filesys fs.FS, targets *[]string) {
	for _, target := range *targets {
		target = filesys.Clean(target)
		addADSStreams(target, targets)
	}
}

// processTarget processes each target in the loop.
// In case of windows the clean up of target is already done
// in preProcessTargets before the loop, hence this is no-op.
func processTarget(_ fs.FS, target string) string {
	return target
}

// SaveDir stores a directory in the repo and returns the node. snPath is the
// path within the current snapshot. In case of windows it also adds the ads
// files as top level nodes. We selectively filter out the ads files later
// for functionalities like totalCount, fileCount, filtering etc.
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
	//In case of windows we want to add the ADS paths as well before sorting.
	paths := getPathsIncludingADS(arch, dir, names)
	sort.Strings(paths)

	nodes := make([]FutureNode, 0, len(paths))

	for _, pathname := range paths {
		// test if context has been cancelled
		if ctx.Err() != nil {
			debug.Log("context has been cancelled, aborting")
			return FutureNode{}, ctx.Err()
		}

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

			return FutureNode{}, errors.Wrap(err, "error saving a target (file or directory)")
		}

		if excluded {
			continue
		}

		nodes = append(nodes, fn)
	}

	fn := arch.treeSaver.Save(ctx, snPath, dir, treeNode, nodes, complete)

	return fn, nil
}

// getPathsIncludingADS iterates all passed path names and adds the ads
// contained in those paths before returning all full paths including ads
func getPathsIncludingADS(arch *Archiver, dir string, names []string) []string {
	paths := make([]string, 0, len(names))

	for _, name := range names {
		pathname := arch.FS.Join(dir, name)
		paths = append(paths, pathname)
		addADSStreams(pathname, &paths)
	}
	return paths
}

// addADSStreams gets the ads streams if any in the pathname passed and adds them to the passed paths
func addADSStreams(pathname string, paths *[]string) {
	success, adsStreams, err := fs.GetADStreamNames(pathname)
	if success {
		streamCount := len(adsStreams)
		if streamCount > 0 {
			debug.Log("ADS Streams for file: %s, streams: %v", pathname, adsStreams)
			for i := 0; i < streamCount; i++ {
				adsStream := adsStreams[i]
				adsPath := pathname + adsStream
				*paths = append(*paths, adsPath)
			}
		}
	} else if err != nil {
		debug.Log("No ADS found for path: %s, err: %v", pathname, err)
	}
}

// processTargets in windows performs Lstat for the ADS files since the file info would not be available for them yet.
func (arch *Archiver) processTargets(target string, targetMain string, abstarget string, fiMain os.FileInfo) (fi os.FileInfo, shouldReturn bool, fn FutureNode, excluded bool, err error) {
	if target != targetMain {
		//If this is an ADS file we need to Lstat again for the file info.
		fi, err = arch.FS.Lstat(target)
		if err != nil {
			debug.Log("lstat() for %v returned error: %v", target, err)
			err = arch.error(abstarget, err)
			if err != nil {
				return nil, true, FutureNode{}, false, errors.WithStack(err)
			}
			//If this is an ads file, shouldReturn should be true because we want to
			// skip the remaining processing of the file.
			return nil, true, FutureNode{}, true, nil
		}
	} else {
		fi = fiMain
	}
	return fi, false, FutureNode{}, false, nil
}
