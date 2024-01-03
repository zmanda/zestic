package restic

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/restic/restic/internal/fs"

	"github.com/restic/restic/internal/debug"
	"github.com/restic/restic/internal/errors"
	"golang.org/x/sys/windows"
)

var (
	once                     sync.Once
	noBackupRestorePrivilege bool
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
func (node Node) restoreExtendedAttributes(path string) error {
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
func (node *Node) fillExtendedAttributes(path string) error {
	var fileHandle windows.Handle
	var err error

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
	extAtts, errEA := fs.GetFileEA(fileHandle)
	debug.Log("fillExtendedAttributes(%v) %v", path, extAtts)
	if errEA != nil {
		debug.Log("open file failed for path: %s : %v", path, errEA)
		return errEA
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
func (node Node) restoreGenericAttributes(path string) error {
	var errGen error
	for _, attr := range node.GenericAttributes {
		if err := attr.restoreGenericAttribute(path); err != nil {
			errGen = fmt.Errorf("Error restoring generic attribute for: %s : %v", path, err)
			debug.Log("%v", errGen)
		}
	}
	return errGen
}

// fillGenericAttributes fills in the generic attributes for windows like FileAttributes,
// Created time and SecurityDescriptor.
func (node *Node) fillGenericAttributes(path string, fi os.FileInfo, stat *statT) error {
	if strings.Contains(filepath.Base(path), ":") || strings.HasSuffix(filepath.Clean(path), `\`) {
		//Do not process for windows directories like C:, D: and for Alternate Data Streams in Windows
		//Filepath.Clean(path) ends with '\' for Windows root drives only.
		return nil
	}
	// Add File Attributes
	node.appendGenericAttribute(getFileAttributes(stat.FileAttributes))

	//Add Creation Time
	node.appendGenericAttribute(getCreationTime(fi, path))

	var err error
	if node.Type == "file" || node.Type == "dir" {
		sd, err := getSecurityDescriptor(path)
		if err == nil {
			//Add Security Descriptor
			node.appendGenericAttribute(sd)
		}
	}
	return err
}

func (node *Node) appendGenericAttribute(extAttr GenericAttribute) {
	if extAttr.Name != "" {
		node.GenericAttributes = append(node.GenericAttributes, extAttr)
	}
}

func getFileAttributes(fileattr uint32) GenericAttribute {
	fileAttrData := make([]byte, 4)
	binary.LittleEndian.PutUint32(fileAttrData, fileattr)
	extAttr := NewGenericAttribute(TypeFileAttribute, fileAttrData)
	return extAttr
}

func getCreationTime(fi os.FileInfo, path string) GenericAttribute {
	var creationTimeAttr GenericAttribute
	attrib, success := fi.Sys().(*syscall.Win32FileAttributeData)
	if success && attrib != nil {
		var creationTime [8]byte
		binary.LittleEndian.PutUint32(creationTime[0:4], attrib.CreationTime.LowDateTime)
		binary.LittleEndian.PutUint32(creationTime[4:8], attrib.CreationTime.HighDateTime)
		creationTimeAttr = NewGenericAttribute(TypeCreationTime, creationTime[:])
	} else {
		debug.Log("Could not get create time for path: %s", path)
	}
	return creationTimeAttr
}

func getSecurityDescriptor(path string) (GenericAttribute, error) {
	var securityDescriptorAttr GenericAttribute
	if noBackupRestorePrivilege {
		//Shortcircuiting since it is already confirmed that there is no backup/restore privilege for SecurityDescriptors.
		return securityDescriptorAttr, nil
	}
	sd, err := fs.GetFileSecurityDescriptor(path)
	if err != nil {
		if errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
			once.Do(handleNoSDBackupRestorePrivileges)
		} else {
			//If backup privilege was already enabled, then this is not an initialization issue as admin permission would be needed for this step.
			//This is a specific error, logging it in debug for now.
			err = fmt.Errorf("Error getting file SecurityDescriptor for: %s : %v", path, err)
			debug.Log("%v", err)
			return securityDescriptorAttr, err
		}
	} else if sd != "" {
		securityDescriptorAttr = NewGenericAttribute(TypeSecurityDescriptor, []byte(sd))
	}
	return securityDescriptorAttr, nil
}

// restoreExtendedAttributes handles restore of the Windows Extended Attributes to the specified path.
// The windows api requires setting of all the extended attributes in one call.
func restoreExtendedAttributes(nodeType, path string, eas []fs.ExtendedAttribute) error {
	var fileHandle windows.Handle
	var err error
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

func handleFileAttributes(path string, data []byte) error {
	attrs := binary.LittleEndian.Uint32(data)
	pathPointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return syscall.SetFileAttributes(pathPointer, attrs)
}

func handleCreationTime(path string, data []byte) error {
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
	if noBackupRestorePrivilege {
		// Shortcircuiting since it is already confirmed that there is no backup/restore
		// privilege for SecurityDescriptors.
		return nil
	}
	sd := string(data)

	if err := fs.SetFileSecurityDescriptor(path, sd); err != nil {
		if errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
			once.Do(handleNoSDBackupRestorePrivileges)
		}
		return err
	}
	return nil
}

// If there are no privileges for backup/restore of SecurityDescriptors, show a warning on Stderr.
func handleNoSDBackupRestorePrivileges() {
	noBackupRestorePrivilege = true
	msg := "WARNING: No privileges for getting/setting file SecurityDescriptors. Run this process as an admin or with `SeBackupPrivilege`, `SeRestorePrivilege` and `SeSecurityPrivilege` for SecurityDescriptor backups/restores to succeed."
	fmt.Fprintln(os.Stderr, msg)
	debug.Log(msg)
}
