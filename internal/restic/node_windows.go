package restic

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/restic/restic/internal/fs"

	"github.com/restic/restic/internal/debug"
	"github.com/restic/restic/internal/errors"
)

// mknod is not supported on Windows.
func mknod(_ string, mode uint32, dev uint64) (err error) {
	return errors.New("device nodes cannot be created on windows")
}

// Windows doesn't need lchown
func lchown(_ string, uid int, gid int) (err error) {
	return nil
}

// restoreSymlinkTimestamps restores timestamps for symlinks
// restoreSymlinkTimestamps restores timestamps for symlinks
func (node Node) restoreSymlinkTimestamps(path string, utimes [2]syscall.Timespec) error {
	// tweaked version of UtimesNano from go/src/syscall/syscall_windows.go
	pathp, e := syscall.UTF16PtrFromString(path)
	if e != nil {
		return e
	}
	h, e := syscall.CreateFile(pathp,
		syscall.FILE_WRITE_ATTRIBUTES, syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING,
		syscall.FILE_FLAG_BACKUP_SEMANTICS|syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if e != nil {
		return e
	}

	defer func() {
		err := syscall.Close(h)
		if err != nil {
			debug.Log("Error closing file handle for %s: %v\n", path, err)
		}
	}()

	a := syscall.NsecToFiletime(syscall.TimespecToNsec(utimes[0]))
	w := syscall.NsecToFiletime(syscall.TimespecToNsec(utimes[1]))
	return syscall.SetFileTime(h, nil, &a, &w)
}

// Getxattr retrieves extended attribute data associated with path.
func Getxattr(path, name string) ([]byte, error) {
	return nil, nil
}

// Listxattr retrieves a list of names of extended attributes associated with the
// given path in the file system.
func Listxattr(path string) ([]string, error) {
	return nil, nil
}

// Setxattr associates name and data together as an attribute of path.
func Setxattr(path, name string, data []byte) error {
	return nil
}

type statT syscall.Win32FileAttributeData

func toStatT(i interface{}) (*statT, bool) {
	s, ok := i.(*syscall.Win32FileAttributeData)
	if ok && s != nil {
		return (*statT)(s), true
	}
	return nil, false
}

func (s statT) dev() uint64   { return 0 }
func (s statT) ino() uint64   { return 0 }
func (s statT) nlink() uint64 { return 0 }
func (s statT) uid() uint32   { return 0 }
func (s statT) gid() uint32   { return 0 }
func (s statT) rdev() uint64  { return 0 }

func (s statT) size() int64 {
	return int64(s.FileSizeLow) | (int64(s.FileSizeHigh) << 32)
}

func (s statT) atim() syscall.Timespec {
	return syscall.NsecToTimespec(s.LastAccessTime.Nanoseconds())
}

func (s statT) mtim() syscall.Timespec {
	return syscall.NsecToTimespec(s.LastWriteTime.Nanoseconds())
}

func (s statT) ctim() syscall.Timespec {
	// Windows does not have the concept of a "change time" in the sense Unix uses it, so we're using the LastWriteTime here.
	return syscall.NsecToTimespec(s.LastWriteTime.Nanoseconds())
}

// restoreGenericAttributes restores generic attributes for Windows
func (node Node) restoreGenericAttributes(path string) (err error) {
	for _, attr := range node.GenericAttributes {
		if errGen := attr.restoreGenericAttribute(path); errGen != nil {
			err = fmt.Errorf("Error restoring generic attribute for: %s : %v", path, errGen)
			debug.Log("%v", err)
		}
	}
	return err
}

// fillGenericAttributes fills in the generic attributes for windows like File Attributes,
// Created time, Security Descriptor etc.
func (node *Node) fillGenericAttributes(path string, fi os.FileInfo, stat *statT) (allowExtended bool, err error) {
	if strings.Contains(filepath.Base(path), ":") {
		//Do not process for Alternate Data Streams in Windows
		// Also do not allow processing of extended attributes for ADS.
		return false, nil
	}
	if !strings.HasSuffix(filepath.Clean(path), `\`) {
		// Do not process file attributes and created time for windows directories like
		// C:, D:
		// Filepath.Clean(path) ends with '\' for Windows root drives only.

		// Add File Attributes
		node.appendGenericAttribute(getFileAttributes(stat.FileAttributes))

		//Add Creation Time
		node.appendGenericAttribute(getCreationTime(fi, path))
	}

	if node.Type == "file" || node.Type == "dir" {
		sd, err := getSecurityDescriptor(path)
		if err == nil {
			//Add Security Descriptor
			node.appendGenericAttribute(sd)
		}
	}
	return true, err
}

// appendGenericAttribute appends a GenericAttribute to the node
func (node *Node) appendGenericAttribute(genericAttribute GenericAttribute) {
	if genericAttribute.Name != "" {
		node.GenericAttributes = append(node.GenericAttributes, genericAttribute)
	}
}

// getFileAttributes gets the value for the GenericAttribute TypeFileAttribute
func getFileAttributes(fileattr uint32) (fileAttribute GenericAttribute) {
	fileAttrData := UInt32ToBytes(fileattr)
	fileAttribute = NewGenericAttribute(TypeFileAttribute, fileAttrData)
	return fileAttribute
}

// UInt32ToBytes converts a uint32 value to a byte array
func UInt32ToBytes(value uint32) (bytes []byte) {
	bytes = make([]byte, 4)
	binary.LittleEndian.PutUint32(bytes, value)
	return bytes
}

// getCreationTime gets the value for the GenericAttribute TypeCreationTime in a windows specific time format.
// The value is a 64-bit value representing the number of 100-nanosecond intervals since January 1, 1601 (UTC)
// split into two 32-bit parts: the low-order DWORD and the high-order DWORD for efficiency and interoperability.
// The low-order DWORD represents the number of 100-nanosecond intervals elapsed since January 1, 1601, modulo
// 2^32. The high-order DWORD represents the number of times the low-order DWORD has overflowed.
func getCreationTime(fi os.FileInfo, path string) (creationTimeAttribute GenericAttribute) {
	attrib, success := fi.Sys().(*syscall.Win32FileAttributeData)
	if success && attrib != nil {
		var creationTime [8]byte
		binary.LittleEndian.PutUint32(creationTime[0:4], attrib.CreationTime.LowDateTime)
		binary.LittleEndian.PutUint32(creationTime[4:8], attrib.CreationTime.HighDateTime)
		creationTimeAttribute = NewGenericAttribute(TypeCreationTime, creationTime[:])
	} else {
		debug.Log("Could not get create time for path: %s", path)
	}
	return creationTimeAttribute
}

// getSecurityDescriptor function retrieves the GenericAttribute containing the byte representation
// of the Security Descriptor. This byte representation is obtained from the encoded string form of
// the raw binary Security Descriptor associated with the Windows file or folder.
func getSecurityDescriptor(path string) (sdAttribute GenericAttribute, err error) {
	sd, err := fs.GetFileSecurityDescriptor(path)
	if err != nil {
		//If backup privilege was already enabled, then this is not an initialization issue as admin permission would be needed for this step.
		//This is a specific error, logging it in debug for now.
		err = fmt.Errorf("Error getting file SecurityDescriptor for: %s : %v", path, err)
		debug.Log("%v", err)
		return sdAttribute, err
	} else if sd != "" {
		sdAttribute = NewGenericAttribute(TypeSecurityDescriptor, []byte(sd))
	}
	return sdAttribute, nil
}

// restoreGenericAttribute restores the generic attributes for Windows like File Attributes,
// Created time, Security Descriptor etc.
func (attr GenericAttribute) restoreGenericAttribute(path string) error {
	switch attr.Name {
	case string(TypeFileAttribute):
		return handleFileAttributes(path, attr.Value)
	case string(TypeCreationTime):
		return handleCreationTime(path, attr.Value)
	case string(TypeSecurityDescriptor):
		return handleSecurityDescriptor(path, attr.Value)
	}
	handleUnknownGenericAttributeFound(attr.Name)
	return nil
}

// handleFileAttributes gets the File Attributes from the data and sets them to the file/folder
// at the specified path.
func handleFileAttributes(path string, data []byte) (err error) {
	attrs := binary.LittleEndian.Uint32(data)
	pathPointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return syscall.SetFileAttributes(pathPointer, attrs)
}

// handleCreationTime gets the creation time from the data and sets it to the file/folder at
// the specified path.
func handleCreationTime(path string, data []byte) (err error) {
	pathPointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	handle, err := syscall.CreateFile(pathPointer,
		syscall.FILE_WRITE_ATTRIBUTES, syscall.FILE_SHARE_WRITE, nil,
		syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return err
	}
	defer func() {
		err := syscall.Close(handle)
		if err != nil {
			debug.Log("Error closing file handle for %s: %v\n", path, err)
		}
	}()

	var inputData bytes.Buffer
	inputData.Write(data)

	var creationTime syscall.Filetime
	creationTime.LowDateTime = binary.LittleEndian.Uint32(data[0:4])
	creationTime.HighDateTime = binary.LittleEndian.Uint32(data[4:8])
	if err := syscall.SetFileTime(handle, &creationTime, nil, nil); err != nil {
		return err
	}
	return nil
}

// handleSecurityDescriptor gets the Security Descriptor from the data and sets it to the file/folder at
// the specified path.
func handleSecurityDescriptor(path string, data []byte) error {
	sd := string(data)

	err := fs.SetFileSecurityDescriptor(path, sd)
	return err
}
