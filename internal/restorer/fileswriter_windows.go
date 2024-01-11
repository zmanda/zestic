package restorer

import (
	"encoding/binary"
	"os"

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
	var flags int
	// If a file has ADS, we will have a GenericAttribute of
	// type TypeHasADS added in the file with values as a string of ads stream names
	// and we will do the following only if the TypeHasADS attribute is found in the node.
	// Otherwise we will directly just use the create option while opening the file.
	if createSize >= 0 {
		var hasAds bool
		var encrypted bool
		attrs := fileInfo.attrs
		if len(attrs) > 0 {
			hasAds = restic.GetGenericAttribute(restic.TypeHasADS, attrs) != nil
			fileAttrBytes := restic.GetGenericAttribute(restic.TypeFileAttribute, attrs)
			if len(fileAttrBytes) > 0 {
				fileAttr := binary.LittleEndian.Uint32(fileAttrBytes)
				encrypted = fileAttr&windows.FILE_ATTRIBUTE_ENCRYPTED != 0
			}
		}

		if encrypted {
			var ptr *uint16
			ptr, err = windows.UTF16PtrFromString(path)
			if err != nil {
				return nil, err
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
	return file, err
}
