package restorer

import (
	"encoding/binary"
	"os"

	"github.com/restic/restic/internal/fs"
	"github.com/restic/restic/internal/restic"
	"golang.org/x/sys/windows"
)

// OpenFile opens the file with truncate and write only options.
// In case of windows, it first attempts to open an existing file,
// and only if the file does not exist, it opens it with create option
// in order to create the file. This is done, otherwise if the ads stream
// is written first (in which case it automatically creates an empty main
// file before writing the stream) and then when the main file is written
// later, the ads stream can be overwritten.
func (fw *filesWriter) OpenFile(createSize int64, path string, fileInfo *fileInfo) (file *os.File, err error) {
	var fileAttributes uint32
	var isAds bool
	fileAttributes, isAds, file, err = fw.openFileImpl(createSize, path, fileInfo)
	if err != nil && isAccessDeniedError(err) {
		// Access is denied, remove readonly and try again.
		err = setReadonlyIfNeeded(fileAttributes, isAds, path)
		if err != nil {
			_, _, file, err = fw.openFileImpl(createSize, path, fileInfo)
			if err != nil {
				return nil, err
			}
		}
	}
	return file, err
}

func (fw *filesWriter) openFileImpl(createSize int64, path string, fileInfo *fileInfo) (fileAttributes uint32, isAds bool, file *os.File, err error) {
	var flags int
	if createSize >= 0 {
		var hasAds, isEncrypted bool
		fileAttributes, hasAds, isAds, isEncrypted = checkGenericAttributes(fileInfo, path)
		if err != nil {
			return fileAttributes, isAds, nil, err
		}

		if isEncrypted {
			var ptr *uint16
			ptr, err = windows.UTF16PtrFromString(path)
			if err != nil {
				return fileAttributes, isAds, nil, err
			}
			// If the file is EFS encrypted, create it by specifying FILE_ATTRIBUTE_ENCRYPTED to windows.CreateFile
			var handle windows.Handle
			handle, err = windows.CreateFile(ptr, uint32(windows.GENERIC_READ|windows.GENERIC_WRITE), uint32(1), nil, uint32(windows.CREATE_ALWAYS), uint32(windows.FILE_ATTRIBUTE_ENCRYPTED), 0)
			if err == nil {
				err = windows.CloseHandle(handle)
			}

			if err == nil {
				flags = os.O_TRUNC | os.O_WRONLY
				file, err = os.OpenFile(path, flags, 0600)
			}
		} else if hasAds {
			// We need to check for existing file before writing only for ads main file
			// so that we do not overwrite any ads streams which may have created the main file
			flags = os.O_TRUNC | os.O_WRONLY
			file, err = os.OpenFile(path, flags, 0600)
			if err != nil && os.IsNotExist(err) {
				//If file not exists open with create flag
				flags = os.O_CREATE | os.O_TRUNC | os.O_WRONLY
				file, err = os.OpenFile(path, flags, 0600)
			}
		} else {
			file, err = fw.openFile(createSize, path, fileInfo)
		}
	} else {
		flags = os.O_WRONLY
		file, err = os.OpenFile(path, flags, 0600)
	}
	return fileAttributes, isAds, file, err
}

// checkGenericAttributes checks for the required generic attributes
func checkGenericAttributes(fileInfo *fileInfo, path string) (fileAttributes uint32, hasAds bool, isAds bool, isEncrypted bool) {
	attrs := fileInfo.attrs
	if len(attrs) > 0 {
		hasAds = restic.GetGenericAttribute(restic.TypeHasADS, attrs) != nil
		isAds = restic.GetGenericAttribute(restic.TypeIsADS, attrs) != nil
		fileAttrBytes := restic.GetGenericAttribute(restic.TypeFileAttribute, attrs)
		if len(fileAttrBytes) > 0 {
			fileAttributes := binary.LittleEndian.Uint32(fileAttrBytes)
			isEncrypted = fileAttributes&windows.FILE_ATTRIBUTE_ENCRYPTED != 0

		}
	}
	return fileAttributes, hasAds, isAds, isEncrypted
}

// setReadonlyIfNeeded check if file is readonly, and then removes the readonly flag
// as it will be set while applying metadata in the next pass.
func setReadonlyIfNeeded(fileAttributes uint32, isAds bool, path string) error {
	readonly := fileAttributes&windows.FILE_ATTRIBUTE_READONLY != 0
	if readonly {
		if isAds {
			// If this is an ads stream we need to get the main file for setting attributes.
			path = fs.TrimAds(path)
		}
		ptr, err := windows.UTF16PtrFromString(path)
		if err != nil {
			return err
		}
		err = windows.SetFileAttributes(ptr, fileAttributes^windows.FILE_ATTRIBUTE_READONLY)
		if err != nil {
			return err
		}
	}
	return nil
}

// isAccessDeniedError checks if the error is ERROR_ACCESS_DENIED
func isAccessDeniedError(err error) bool {
	return fs.IsErrorOfType(err, windows.ERROR_ACCESS_DENIED)
}
