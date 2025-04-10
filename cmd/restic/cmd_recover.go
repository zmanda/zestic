package main

import (
	"context"
	"os"
	"time"

	"github.com/restic/restic/internal/errors"
	"github.com/restic/restic/internal/repository"
	"github.com/restic/restic/internal/restic"
	"github.com/restic/restic/internal/ui/progress"
	"github.com/restic/restic/internal/ui/termstatus"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
)

func newRecoverCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "recover [flags]",
		Short: "Recover data from the repository not referenced by snapshots",
		Long: `
The "recover" command builds a new snapshot from all directories it can find in
the raw data of the repository which are not referenced in an existing snapshot.
It can be used if, for example, a snapshot has been removed by accident with "forget".

EXIT STATUS
===========

Exit status is 0 if the command was successful.
Exit status is 1 if there was any error.
Exit status is 10 if the repository does not exist.
Exit status is 11 if the repository is already locked.
Exit status is 12 if the password is incorrect.
`,
		GroupID:           cmdGroupDefault,
		DisableAutoGenTag: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			term, cancel := setupTermstatus()
			defer cancel()
			return runRecover(cmd.Context(), globalOptions, term)
		},
	}
	return cmd
}

func runRecover(ctx context.Context, gopts GlobalOptions, term *termstatus.Terminal) error {
	hostname, err := os.Hostname()
	if err != nil {
		return err
	}

	ctx, repo, unlock, err := openWithExclusiveLock(ctx, gopts, false)
	if err != nil {
		return err
	}
	defer unlock()

	printer := newTerminalProgressPrinter(gopts.verbosity, term)

	snapshotLister, err := restic.MemorizeList(ctx, repo, restic.SnapshotFile)
	if err != nil {
		return err
	}

	printer.P("ensuring index is complete\n")
	err = repository.RepairIndex(ctx, repo, repository.RepairIndexOptions{}, printer)
	if err != nil {
		return err
	}

	printer.P("load index files\n")
	bar := newIndexTerminalProgress(gopts.Quiet, gopts.JSON, term)
	if err = repo.LoadIndex(ctx, bar); err != nil {
		return err
	}

	// trees maps a tree ID to whether or not it is referenced by a different
	// tree. If it is not referenced, we have a root tree.
	trees := make(map[restic.ID]bool)

	err = repo.ListBlobs(ctx, func(blob restic.PackedBlob) {
		if blob.Type == restic.TreeBlob {
			trees[blob.Blob.ID] = false
		}
	})
	if err != nil {
		return err
	}

	printer.P("load %d trees\n", len(trees))
	bar = newTerminalProgressMax(!gopts.Quiet, uint64(len(trees)), "trees loaded", term)
	for id := range trees {
		tree, err := restic.LoadTree(ctx, repo, id)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			printer.E("unable to load tree %v: %v\n", id.Str(), err)
			continue
		}

		for _, node := range tree.Nodes {
			if node.Type == restic.NodeTypeDir && node.Subtree != nil {
				trees[*node.Subtree] = true
			}
		}
		bar.Add(1)
	}
	bar.Done()

	printer.P("load snapshots\n")
	err = restic.ForAllSnapshots(ctx, snapshotLister, repo, nil, func(_ restic.ID, sn *restic.Snapshot, _ error) error {
		trees[*sn.Tree] = true
		return nil
	})
	if err != nil {
		return err
	}
	printer.P("done\n")

	roots := restic.NewIDSet()
	for id, seen := range trees {
		if !seen {
			printer.V("found root tree %v\n", id.Str())
			roots.Insert(id)
		}
	}
	printer.S("\nfound %d unreferenced roots\n", len(roots))

	if len(roots) == 0 {
		printer.P("no snapshot to write.\n")
		return nil
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}

	tree := restic.NewTree(len(roots))
	for id := range roots {
		var subtreeID = id
		node := restic.Node{
			Type:       restic.NodeTypeDir,
			Name:       id.Str(),
			Mode:       0755,
			Subtree:    &subtreeID,
			AccessTime: time.Now(),
			ModTime:    time.Now(),
			ChangeTime: time.Now(),
		}
		err := tree.Insert(&node)
		if err != nil {
			return err
		}
	}

	wg, wgCtx := errgroup.WithContext(ctx)
	repo.StartPackUploader(wgCtx, wg)

	var treeID restic.ID
	wg.Go(func() error {
		var err error
		treeID, err = restic.SaveTree(wgCtx, repo, tree)
		if err != nil {
			return errors.Fatalf("unable to save new tree to the repository: %v", err)
		}

		err = repo.Flush(wgCtx)
		if err != nil {
			return errors.Fatalf("unable to save blobs to the repository: %v", err)
		}
		return nil
	})
	err = wg.Wait()
	if err != nil {
		return err
	}

	return createSnapshot(ctx, printer, "/recover", hostname, []string{"recovered"}, repo, &treeID)

}

func createSnapshot(ctx context.Context, printer progress.Printer, name, hostname string, tags []string, repo restic.SaverUnpacked[restic.WriteableFileType], tree *restic.ID) error {
	sn, err := restic.NewSnapshot([]string{name}, tags, hostname, time.Now())
	if err != nil {
		return errors.Fatalf("unable to save snapshot: %v", err)
	}

	sn.Tree = tree

	id, err := restic.SaveSnapshot(ctx, repo, sn)
	if err != nil {
		return errors.Fatalf("unable to save snapshot: %v", err)
	}

	printer.S("saved new snapshot %v\n", id.Str())
	return nil
}
