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
	"golang.org/x/sys/windows"
)

// mknod is not supported on Windows.
func mknod(_ string, mode uint32, dev uint64) (err error) {
	return errors.New("device nodes cannot be created on windows")
}

// Windows doesn't need lchown
func lchown(_ string, uid int, gid int) (err error) {
	return nil
}

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
	return s.mtim()
}

// restore extended attributes for windows
func (node Node) restoreExtendedAttributes(path string) (err error) {
	eas := []fs.ExtendedAttribute{}
	for _, attr := range node.ExtendedAttributes {
		extr := new(fs.ExtendedAttribute)
		extr.Name = attr.Name
		extr.Value = attr.Value
		eas = append(eas, *extr)
	}
	if len(eas) > 0 {
		if errExt := restoreExtendedAttributes(node.Type, path, eas); errExt != nil {
			return errExt
		}
	}
	return nil
}

// fill extended attributes in the node. This also includes the Generic attributes for windows.
func (node *Node) fillExtendedAttributes(path string) (err error) {
	var fileHandle windows.Handle

	//Get file handle for file or dir
	if node.Type == "file" {
		if strings.HasSuffix(filepath.Clean(path), `\`) {
			return nil
		}
		utf16Path := windows.StringToUTF16Ptr(path)
		fileAccessRightReadWriteEA := (0x8 | 0x10)
		fileHandle, err = windows.CreateFile(utf16Path, uint32(fileAccessRightReadWriteEA), 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	} else if node.Type == "dir" {
		utf16Path := windows.StringToUTF16Ptr(path)
		fileAccessRightReadWriteEA := (0x8 | 0x10)
		fileHandle, err = windows.CreateFile(utf16Path, uint32(fileAccessRightReadWriteEA), 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	} else {
		return nil
	}
	if err != nil {
		err = errors.Errorf("open file failed for path: %s, with: %v", path, err)
		return err
	}
	defer func() {
		err := windows.CloseHandle(fileHandle)
		if err != nil {
			debug.Log("Error closing file handle for %s: %v\n", path, err)
		}
	}()

	//Get the windows Extended Attributes using the file handle
	extAtts, err := fs.GetFileEA(fileHandle)
	debug.Log("fillExtendedAttributes(%v) %v", path, extAtts)
	if err != nil {
		debug.Log("open file failed for path: %s : %v", path, err)
		return err
	} else if len(extAtts) == 0 {
		return nil
	}

	//Fill the ExtendedAttributes in the node using the name/value pairs in the windows EA
	for _, attr := range extAtts {
		if err != nil {
			err = errors.Errorf("can not obtain extended attribute for path %v, attr: %v, err: %v\n,", path, attr, err)
			continue
		}
		extendedAttr := ExtendedAttribute{
			Name:  attr.Name,
			Value: attr.Value,
		}

		node.ExtendedAttributes = append(node.ExtendedAttributes, extendedAttr)
	}
	return nil
}

// restoreGenericAttributes restores generic attributes for windows
func (node Node) restoreGenericAttributes(path string) (err error) {
	for _, attr := range node.GenericAttributes {
		if errGen := attr.restoreGenericAttribute(path); errGen != nil {
			err = fmt.Errorf("Error restoring generic attribute for: %s : %v", path, errGen)
			debug.Log("%v", err)
		}
	}
	return err
}

// fillGenericAttributes fills in the generic attributes for windows like FileAttributes,
// Created time and SecurityDescriptor.
func (node *Node) fillGenericAttributes(path string, fi os.FileInfo, stat *statT) (allowExtended bool, err error) {
	if strings.Contains(filepath.Base(path), ":") || strings.HasSuffix(filepath.Clean(path), `\`) {
		//Do not process for windows directories like C:, D: and for Alternate Data Streams in Windows
		//Filepath.Clean(path) ends with '\' for Windows root drives only.
		// Also do not allow to process extended attributes.
		return false, nil
	}
	// Add File Attributes
	node.appendGenericAttribute(getFileAttributes(stat.FileAttributes))

	//Add Creation Time
	node.appendGenericAttribute(getCreationTime(fi, path))

	if node.Type == "file" || node.Type == "dir" {
		sd, err := getSecurityDescriptor(path)
		if err == nil {
			//Add Security Descriptor
			node.appendGenericAttribute(sd)
		}
	}
	return true, err
}

func (node *Node) appendGenericAttribute(genericAttribute GenericAttribute) {
	if genericAttribute.Name != "" {
		node.GenericAttributes = append(node.GenericAttributes, genericAttribute)
	}
}

func getFileAttributes(fileattr uint32) (fileAttribute GenericAttribute) {
	fileAttrData := make([]byte, 4)
	binary.LittleEndian.PutUint32(fileAttrData, fileattr)
	fileAttribute = NewGenericAttribute(TypeFileAttribute, fileAttrData)
	return fileAttribute
}

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

// restoreExtendedAttributes handles restore of the Windows Extended Attributes to the specified path.
// The windows api requires setting of all the extended attributes in one call.
func restoreExtendedAttributes(nodeType, path string, eas []fs.ExtendedAttribute) (err error) {
	var fileHandle windows.Handle
	switch nodeType {
	case "file":
		utf16Path := windows.StringToUTF16Ptr(path)
		fileAccessRightReadWriteEA := (0x8 | 0x10)
		fileHandle, err = windows.CreateFile(utf16Path, uint32(fileAccessRightReadWriteEA), 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	case "dir":
		utf16Path := windows.StringToUTF16Ptr(path)
		fileAccessRightReadWriteEA := (0x8 | 0x10)
		fileHandle, err = windows.CreateFile(utf16Path, uint32(fileAccessRightReadWriteEA), 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	default:
		return nil
	}
	defer func() {
		err := windows.CloseHandle(fileHandle)
		if err != nil {
			debug.Log("Error closing file handle for %s: %v\n", path, err)
		}
	}()
	if err != nil {
		err = errors.Errorf("open file failed for path %v, with: %v:\n", path, err)
	} else if err = fs.SetFileEA(fileHandle, eas); err != nil {
		err = errors.Errorf("set EA failed for path %v, with: %v:\n", path, err)
	}
	return err
}

// restoreGenericAttribute restores the generic attributes for windows like File Attributes,
// Created time and Security Descriptors.
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

func handleFileAttributes(path string, data []byte) (err error) {
	attrs := binary.LittleEndian.Uint32(data)
	pathPointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return syscall.SetFileAttributes(pathPointer, attrs)
}

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

func handleSecurityDescriptor(path string, data []byte) error {
	sd := string(data)

	err := fs.SetFileSecurityDescriptor(path, sd)
	return err
}
