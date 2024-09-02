package util

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/cenkalti/backoff/v4"
	"github.com/restic/restic/internal/backend"
	"github.com/restic/restic/internal/backend/layout"
	"github.com/restic/restic/internal/debug"
	"github.com/restic/restic/internal/errors"
	"github.com/restic/restic/internal/fs"
)

// File is common interface for os.File and smb.File
type File interface {
	Close() error
	Name() string
	Read(p []byte) (n int, err error)
	Readdir(count int) ([]os.FileInfo, error)
	Readdirnames(n int) ([]string, error)
	Seek(offset int64, whence int) (int64, error)
	Stat() (os.FileInfo, error)
	Sync() error
	Write(p []byte) (n int, err error)
}

var errTooShort = fmt.Errorf("file is too short")

func DeriveModesFromStat(l layout.Layout, statFn func(string) (os.FileInfo, error)) Modes {
	fi, err := statFn(l.Filename(backend.Handle{Type: backend.ConfigFile}))
	m := DeriveModesFromFileInfo(fi, err)
	debug.Log("using (%03O file, %03O dir) permissions", m.File, m.Dir)
	return m
}

// Create creates all the necessary files and directories for a new local
// backend at dir. Afterwards a new config blob should be created.
func Create(fileName string, dirMode os.FileMode, paths []string, lstatFn func(string) (os.FileInfo, error), MkdirAllFn func(string, os.FileMode) error) error {
	// test if config file already exists
	_, err := lstatFn(fileName)
	if err == nil {
		return errors.New("config file already exists")
	}
	// create paths for data and refs
	for _, d := range paths {
		err := MkdirAllFn(d, dirMode)
		if err != nil {
			return errors.WithStack(err)
		}
	}
	return nil
}

// SaveOptions contains options for saving files.
type SaveOptions struct {
	OpenTempFile    func(dir, name string) (File, error)
	MkDir           func(dir string) error
	Remove          func(name string) error
	IsMacENOTTY     func(error) bool
	Rename          func(oldname, newname string) error
	FsyncDir        func(dir string) error
	SetFileReadonly func(name string) error
	DirMode         os.FileMode
	FileMode        os.FileMode
}

// SaveWithOptions stores data in the backend at the handle using the provided options.
func SaveWithOptions(fileName string, tmpFilename string, rd backend.RewindReader, options SaveOptions) (err error) {
	dir := filepath.Dir(fileName)

	defer func() {
		// Mark non-retriable errors as such
		if errors.Is(err, syscall.ENOSPC) || os.IsPermission(err) {
			err = backoff.Permanent(err)
		}
	}()

	f, err := options.OpenTempFile(dir, tmpFilename)

	if IsNotExist(err) {
		debug.Log("error %v: creating dir", err)

		// error is caused by a missing directory, try to create it
		mkdirErr := options.MkDir(dir)
		if mkdirErr != nil {
			debug.Log("error creating dir %v: %v", dir, mkdirErr)
		} else {
			// try again
			f, err = options.OpenTempFile(dir, tmpFilename)
		}
	}

	if err != nil {
		return errors.WithStack(err)
	}

	defer func(f File) {
		if err != nil {
			_ = f.Close() // Double Close is harmless.
			// Remove after Rename is harmless: we embed the final name in the
			// temporary's name and no other goroutine will get the same data to
			// Save, so the temporary name should never be reused by another
			// goroutine.
			_ = options.Remove(f.Name())
		}
	}(f)

	if f, ok := f.(*os.File); ok {
		// preallocate disk space only for os.File
		if size := rd.Length(); size > 0 {
			if err := fs.PreallocateFile(f, size); err != nil {
				debug.Log("Failed to preallocate %v with size %v: %v", fileName, size, err)
			}
		}
	}

	// save data, then sync
	wbytes, err := io.Copy(f, rd)
	if err != nil {
		return errors.WithStack(err)
	}
	// sanity check
	if wbytes != rd.Length() {
		return errors.Errorf("wrote %d bytes instead of the expected %d bytes", wbytes, rd.Length())
	}

	// Ignore error if filesystem does not support fsync.
	err = f.Sync()
	syncNotSup := err != nil && (errors.Is(err, syscall.ENOTSUP) || options.IsMacENOTTY(err))
	if err != nil && !syncNotSup {
		return errors.WithStack(err)
	}

	// Close, then rename. Windows doesn't like the reverse order.
	if err = f.Close(); err != nil {
		return errors.WithStack(err)
	}
	if err = options.Rename(f.Name(), fileName); err != nil {
		return errors.WithStack(err)
	}

	// Now sync the directory to commit the Rename.
	if !syncNotSup {
		err = options.FsyncDir(dir)
		if err != nil {
			return errors.WithStack(err)
		}
	}

	// try to mark file as read-only to avoid accidental modifications
	// ignore if the operation fails as some filesystems don't allow the chmod call
	// e.g. exfat and network file systems with certain mount options
	err = options.SetFileReadonly(fileName)
	if err != nil && !os.IsPermission(err) {
		return errors.WithStack(err)
	}

	return nil
}

func OpenReader(openFile func(string) (File, error), fileName string, length int, offset int64) (io.ReadCloser, error) {
	f, err := openFile(fileName)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}

	size := fi.Size()
	if size < offset+int64(length) {
		_ = f.Close()
		return nil, errTooShort
	}

	if offset > 0 {
		_, err = f.Seek(offset, 0)
		if err != nil {
			_ = f.Close()
			return nil, err
		}
	}

	if length > 0 {
		return LimitReadCloser(f, int64(length)), nil
	}

	return f, nil
}

// Stat returns information about a blob.
func Stat(statFn func(string) (os.FileInfo, error), fileName, handleName string) (backend.FileInfo, error) {
	fi, err := statFn(fileName)
	if err != nil {
		return backend.FileInfo{}, errors.WithStack(err)
	}
	return backend.FileInfo{Size: fi.Size(), Name: handleName}, nil
}

// Remove removes the blob with the given name and type.
func Remove(filename string, chmodfn func(string, os.FileMode) error) error {
	// reset read-only flag
	err := chmodfn(filename, 0666)
	if err != nil && !os.IsPermission(err) {
		return errors.WithStack(err)
	}

	return os.Remove(filename)
}

// List runs fn for each file in the backend which has the type t. When an
// error occurs (or fn returns an error), List stops and returns it.
func List(ctx context.Context, basedir string, subdirs bool, openFunc func(name string) (File, error), t backend.FileType, fn func(backend.FileInfo) error) (err error) {
	if subdirs {
		err = visitDirs(ctx, openFunc, basedir, fn)
	} else {
		err = visitFiles(ctx, openFunc, basedir, fn, false)
	}

	if IsNotExist(err) {
		debug.Log("ignoring non-existing directory")
		return nil
	}

	return err
}

// The following two functions are like filepath.Walk, but visit only one or
// two levels of directory structure (including dir itself as the first level).
// Also, visitDirs assumes it sees a directory full of directories, while
// visitFiles wants a directory full or regular files.
// visitDirs visits directories
func visitDirs(ctx context.Context, openDir func(string) (File, error), dir string, fn func(backend.FileInfo) error) error {
	d, err := openDir(dir)
	if err != nil {
		return err
	}

	sub, err := d.Readdirnames(-1)
	if err != nil {
		// ignore subsequent errors
		_ = d.Close()
		return err
	}

	err = d.Close()
	if err != nil {
		return err
	}

	for _, f := range sub {
		err = visitFiles(ctx, openDir, filepath.Join(dir, f), fn, true)
		if err != nil {
			return err
		}
	}
	return ctx.Err()
}

// visitFiles visits files
func visitFiles(ctx context.Context, openDir func(string) (File, error), dir string, fn func(backend.FileInfo) error, ignoreNotADirectory bool) error {
	d, err := openDir(dir)
	if err != nil {
		return err
	}

	if ignoreNotADirectory {
		fi, err := d.Stat()
		if err != nil || !fi.IsDir() {
			// ignore subsequent errors
			_ = d.Close()
			return err
		}
	}

	sub, err := d.Readdir(-1)
	if err != nil {
		// ignore subsequent errors
		_ = d.Close()
		return err
	}

	err = d.Close()
	if err != nil {
		return err
	}

	for _, fi := range sub {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		err := fn(backend.FileInfo{
			Name: fi.Name(),
			Size: fi.Size(),
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// IsNotExist returns true if the error is caused by a non existing file.
func IsNotExist(err error) bool {
	return errors.Is(err, os.ErrNotExist)
}

// IsPermanentError checks if the error is permanent
func IsPermanentError(err error) bool {
	return IsNotExist(err) || errors.Is(err, errTooShort) || errors.Is(err, os.ErrPermission)
}
