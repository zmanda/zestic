//go:build windows
// +build windows

// The file sd_windows.go was adapted from github.com/Microsoft/go-winio under MIT license.

// The MIT License (MIT)

// Copyright (c) 2015 Microsoft

// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:

// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.

// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

package fs

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"sync"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modadvapi32 = windows.NewLazySystemDLL("advapi32.dll")

	procLookupPrivilegeValueW       = modadvapi32.NewProc("LookupPrivilegeValueW")
	procAdjustTokenPrivileges       = modadvapi32.NewProc("AdjustTokenPrivileges")
	procLookupPrivilegeDisplayNameW = modadvapi32.NewProc("LookupPrivilegeDisplayNameW")
	procLookupPrivilegeNameW        = modadvapi32.NewProc("LookupPrivilegeNameW")
)

// Do the interface allocations only once for common
// Errno values.
const (
	errnoErrorIOPending = 997

	//revive:disable-next-line:var-naming ALL_CAPS
	SE_PRIVILEGE_ENABLED = windows.SE_PRIVILEGE_ENABLED

	//revive:disable-next-line:var-naming ALL_CAPS
	ERROR_NOT_ALL_ASSIGNED syscall.Errno = windows.ERROR_NOT_ALL_ASSIGNED

	SeBackupPrivilege   = "SeBackupPrivilege"
	SeRestorePrivilege  = "SeRestorePrivilege"
	SeSecurityPrivilege = "SeSecurityPrivilege"
)

var (
	errErrorIOPending error = syscall.Errno(errnoErrorIOPending)
	errErrorEinval    error = syscall.EINVAL

	privNames             = make(map[string]uint64)
	privNameMutex         sync.Mutex
	onceBackup            sync.Once
	onceRestore           sync.Once
	backupPrivilegeError  error
	restorePrivilegeError error
)

// PrivilegeError represents an error enabling privileges.
type PrivilegeError struct {
	privileges []uint64
}

// GetFileSecurityDescriptor takes the path of the file
// and returns an encoded string representation of the SecurityDescriptor for the file.
// This needs admin permissions or SeBackupPrivilege to work.
// If there are no admin permissions, a windows.ERROR_PRIVILEGE_NOT_HELD error would be returned.
func GetFileSecurityDescriptor(filePath string) (securityDescriptor string, err error) {
	onceBackup.Do(enableBackupPrivilege)
	if backupPrivilegeError != nil {
		return "", backupPrivilegeError
	}

	utf16Path := windows.StringToUTF16Ptr(filePath)
	fileHandle, err := windows.CreateFile(utf16Path, (windows.READ_CONTROL | windows.ACCESS_SYSTEM_SECURITY), windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", fmt.Errorf("open file failed with: %w", err)
	}
	defer func() {
		_ = windows.CloseHandle(fileHandle)
	}()
	sd, err := windows.GetSecurityInfo(fileHandle, windows.SE_FILE_OBJECT, (windows.ATTRIBUTE_SECURITY_INFORMATION | windows.DACL_SECURITY_INFORMATION | windows.GROUP_SECURITY_INFORMATION | windows.LABEL_SECURITY_INFORMATION | windows.OWNER_SECURITY_INFORMATION | windows.SACL_SECURITY_INFORMATION | windows.SCOPE_SECURITY_INFORMATION | windows.BACKUP_SECURITY_INFORMATION))
	if err != nil {
		return "", fmt.Errorf("get security info failed: %w", err)
	}

	sdBytes, err := securityDescriptorStructToBytes(sd)
	if err != nil {
		return "", fmt.Errorf("convert security descriptor to bytes failed: %w", err)
	}

	securityDescriptor = base64.StdEncoding.EncodeToString(sdBytes)
	return securityDescriptor, nil
}

// SetFileSecurityDescriptor takes the path of the file
// and an encoded string representation of the SecurityDescriptor for the file
// and sets the SecurityDescriptor for the file after decoding the value.
// This needs admin permissions or SeRestorePrivilege and SeSecurityPrivilege to work.
// If there are no admin permissions, a windows.ERROR_PRIVILEGE_NOT_HELD error would be returned.
func SetFileSecurityDescriptor(filePath string, securityDescriptor string) error {
	onceRestore.Do(enableRestorePrivilege)
	if restorePrivilegeError != nil {
		return restorePrivilegeError
	}

	sdBytes, err := base64.StdEncoding.DecodeString(securityDescriptor)
	if err != nil {
		// Not returning sd as-is in the error-case, as base64.DecodeString
		// may return partially decoded data (not nil or empty slice) in case
		// of a failure: https://github.com/golang/go/blob/go1.17.7/src/encoding/base64/base64.go#L382-L387
		return err
	}
	utf16Path := windows.StringToUTF16Ptr(filePath)
	fileHandle, err := windows.CreateFile(utf16Path, windows.WRITE_DAC|windows.WRITE_OWNER|windows.STANDARD_RIGHTS_WRITE|windows.ACCESS_SYSTEM_SECURITY|windows.FILE_LIST_DIRECTORY, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return fmt.Errorf("error getting file handle: %w", err)
	}
	defer func() {
		_ = windows.CloseHandle(fileHandle)
	}()
	// Set the security descriptor on the file
	sd, err := SecurityDescriptorBytesToStruct(sdBytes)
	if err != nil {
		return fmt.Errorf("error converting bytes to security descriptor: %w", err)
	}

	owner, _, err := sd.Owner()
	if err != nil {
		//Do not set partial values.
		owner = nil
	}
	group, _, err := sd.Group()
	if err != nil {
		//Do not set partial values.
		group = nil
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		//Do not set partial values.
		dacl = nil
	}
	sacl, _, err := sd.SACL()
	if err != nil {
		//Do not set partial values.
		sacl = nil
	}

	err = windows.SetSecurityInfo(fileHandle, windows.SE_FILE_OBJECT, (windows.ATTRIBUTE_SECURITY_INFORMATION | windows.DACL_SECURITY_INFORMATION | windows.GROUP_SECURITY_INFORMATION | windows.LABEL_SECURITY_INFORMATION | windows.OWNER_SECURITY_INFORMATION | windows.SACL_SECURITY_INFORMATION | windows.SCOPE_SECURITY_INFORMATION | windows.BACKUP_SECURITY_INFORMATION), owner, group, dacl, sacl)

	if err != nil {
		return fmt.Errorf("error setting security info: %w", err)
	}
	return nil
}

func enableBackupPrivilege() {
	err := enableProcessPrivileges([]string{SeBackupPrivilege})
	if err != nil {
		backupPrivilegeError = fmt.Errorf("error enabling backup privilege: %w", err)
	}
}

func enableRestorePrivilege() {
	err := enableProcessPrivileges([]string{SeRestorePrivilege, SeSecurityPrivilege})
	if err != nil {
		restorePrivilegeError = fmt.Errorf("error enabling restore/security privilege: %w", err)
	}
}

func SecurityDescriptorBytesToStruct(sd []byte) (*windows.SECURITY_DESCRIPTOR, error) {
	if l := int(unsafe.Sizeof(windows.SECURITY_DESCRIPTOR{})); len(sd) < l {
		return nil, fmt.Errorf("securityDescriptor (%d) smaller than expected (%d): %w", len(sd), l, windows.ERROR_INCORRECT_SIZE)
	}
	s := (*windows.SECURITY_DESCRIPTOR)(unsafe.Pointer(&sd[0]))
	return s, nil
}

func securityDescriptorStructToBytes(sd *windows.SECURITY_DESCRIPTOR) ([]byte, error) {
	b := unsafe.Slice((*byte)(unsafe.Pointer(sd)), sd.Length())
	return b, nil
}

func (e *PrivilegeError) Error() string {
	s := "Could not enable privilege "
	if len(e.privileges) > 1 {
		s = "Could not enable privileges "
	}
	for i, p := range e.privileges {
		if i != 0 {
			s += ", "
		}
		s += `"`
		s += getPrivilegeName(p)
		s += `"`
	}
	return s
}

func mapPrivileges(names []string) ([]uint64, error) {
	privileges := make([]uint64, 0, len(names))
	privNameMutex.Lock()
	defer privNameMutex.Unlock()
	for _, name := range names {
		p, ok := privNames[name]
		if !ok {
			err := lookupPrivilegeValue("", name, &p)
			if err != nil {
				return nil, err
			}
			privNames[name] = p
		}
		privileges = append(privileges, p)
	}
	return privileges, nil
}

// enableProcessPrivileges enables privileges globally for the process.
func enableProcessPrivileges(names []string) error {
	return enableDisableProcessPrivilege(names, SE_PRIVILEGE_ENABLED)
}

// disableProcessPrivileges disables privileges globally for the process.
func DisableProcessPrivileges(names []string) error {
	return enableDisableProcessPrivilege(names, 0)
}

// DisableBackupPrivileges disables privileges that are needed for backup operations.
// They may be reenabled if GetFileSecurityDescriptor is called again.
func DisableBackupPrivileges() error {
	//Reset the once so that backup privileges can be enabled again if needed.
	onceBackup = sync.Once{}
	return enableDisableProcessPrivilege([]string{SeBackupPrivilege}, 0)
}

// DisableRestorePrivileges disables privileges that are needed for restore operations.
// They may be reenabled if SetFileSecurityDescriptor is called again.
func DisableRestorePrivileges() error {
	//Reset the once so that restore privileges can be enabled again if needed.
	onceRestore = sync.Once{}
	return enableDisableProcessPrivilege([]string{SeRestorePrivilege, SeSecurityPrivilege}, 0)
}

func enableDisableProcessPrivilege(names []string, action uint32) error {
	privileges, err := mapPrivileges(names)
	if err != nil {
		return err
	}

	p := windows.CurrentProcess()
	var token windows.Token
	err = windows.OpenProcessToken(p, windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &token)
	if err != nil {
		return err
	}

	defer func() {
		_ = token.Close()
	}()
	return adjustPrivileges(token, privileges, action)
}

func adjustPrivileges(token windows.Token, privileges []uint64, action uint32) error {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(privileges)))
	for _, p := range privileges {
		_ = binary.Write(&b, binary.LittleEndian, p)
		_ = binary.Write(&b, binary.LittleEndian, action)
	}
	prevState := make([]byte, b.Len())
	reqSize := uint32(0)
	success, err := adjustTokenPrivileges(token, false, &b.Bytes()[0], uint32(len(prevState)), &prevState[0], &reqSize)
	if !success {
		return err
	}
	if err == ERROR_NOT_ALL_ASSIGNED { //nolint:errorlint // err is Errno
		return &PrivilegeError{privileges}
	}
	return nil
}

func getPrivilegeName(luid uint64) string {
	var nameBuffer [256]uint16
	bufSize := uint32(len(nameBuffer))
	err := lookupPrivilegeName("", &luid, &nameBuffer[0], &bufSize)
	if err != nil {
		return fmt.Sprintf("<unknown privilege %d>", luid)
	}

	var displayNameBuffer [256]uint16
	displayBufSize := uint32(len(displayNameBuffer))
	var langID uint32
	err = lookupPrivilegeDisplayName("", &nameBuffer[0], &displayNameBuffer[0], &displayBufSize, &langID)
	if err != nil {
		return fmt.Sprintf("<unknown privilege %s>", string(utf16.Decode(nameBuffer[:bufSize])))
	}

	return string(utf16.Decode(displayNameBuffer[:displayBufSize]))
}

func adjustTokenPrivileges(token windows.Token, releaseAll bool, input *byte, outputSize uint32, output *byte, requiredSize *uint32) (success bool, err error) {
	var _p0 uint32
	if releaseAll {
		_p0 = 1
	}
	r0, _, e1 := syscall.SyscallN(procAdjustTokenPrivileges.Addr(), uintptr(token), uintptr(_p0), uintptr(unsafe.Pointer(input)), uintptr(outputSize), uintptr(unsafe.Pointer(output)), uintptr(unsafe.Pointer(requiredSize)))
	success = r0 != 0
	if !success {
		err = errnoErr(e1)
	}
	return
}

func lookupPrivilegeDisplayName(systemName string, name *uint16, buffer *uint16, size *uint32, languageID *uint32) (err error) {
	var _p0 *uint16
	_p0, err = syscall.UTF16PtrFromString(systemName)
	if err != nil {
		return
	}
	return _lookupPrivilegeDisplayName(_p0, name, buffer, size, languageID)
}

func _lookupPrivilegeDisplayName(systemName *uint16, name *uint16, buffer *uint16, size *uint32, languageID *uint32) (err error) {
	r1, _, e1 := syscall.SyscallN(procLookupPrivilegeDisplayNameW.Addr(), uintptr(unsafe.Pointer(systemName)), uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(buffer)), uintptr(unsafe.Pointer(size)), uintptr(unsafe.Pointer(languageID)), 0)
	if r1 == 0 {
		err = errnoErr(e1)
	}
	return
}

func lookupPrivilegeName(systemName string, luid *uint64, buffer *uint16, size *uint32) (err error) {
	var _p0 *uint16
	_p0, err = syscall.UTF16PtrFromString(systemName)
	if err != nil {
		return
	}
	return _lookupPrivilegeName(_p0, luid, buffer, size)
}

func _lookupPrivilegeName(systemName *uint16, luid *uint64, buffer *uint16, size *uint32) (err error) {
	r1, _, e1 := syscall.SyscallN(procLookupPrivilegeNameW.Addr(), uintptr(unsafe.Pointer(systemName)), uintptr(unsafe.Pointer(luid)), uintptr(unsafe.Pointer(buffer)), uintptr(unsafe.Pointer(size)), 0, 0)
	if r1 == 0 {
		err = errnoErr(e1)
	}
	return
}

func lookupPrivilegeValue(systemName string, name string, luid *uint64) (err error) {
	var _p0 *uint16
	_p0, err = syscall.UTF16PtrFromString(systemName)
	if err != nil {
		return
	}
	var _p1 *uint16
	_p1, err = syscall.UTF16PtrFromString(name)
	if err != nil {
		return
	}
	return _lookupPrivilegeValue(_p0, _p1, luid)
}

func _lookupPrivilegeValue(systemName *uint16, name *uint16, luid *uint64) (err error) {
	r1, _, e1 := syscall.SyscallN(procLookupPrivilegeValueW.Addr(), uintptr(unsafe.Pointer(systemName)), uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(luid)))
	if r1 == 0 {
		err = errnoErr(e1)
	}
	return
}

// errnoErr returns common boxed Errno values, to prevent
// allocations at runtime.
func errnoErr(e syscall.Errno) error {
	switch e {
	case 0:
		return errErrorEinval
	case errnoErrorIOPending:
		return errErrorIOPending
	}
	// TODO: add more here, after collecting data on the common
	// error values see on Windows. (perhaps when running
	// all.bat?)
	return e
}
