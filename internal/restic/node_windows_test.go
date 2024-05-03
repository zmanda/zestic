//go:build windows
// +build windows

package restic_test

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/restic/restic/internal/errors"
	"github.com/restic/restic/internal/fs"
	"github.com/restic/restic/internal/restic"
	"github.com/restic/restic/internal/test"
	"golang.org/x/sys/windows"
)

func TestRestoreExtendedAttributes(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	expectedNodes := []restic.Node{
		{
			Name:       "testfile",
			Type:       "file",
			Mode:       0644,
			ModTime:    parseTime("2005-05-14 21:07:03.111"),
			AccessTime: parseTime("2005-05-14 21:07:04.222"),
			ChangeTime: parseTime("2005-05-14 21:07:05.333"),
			ExtendedAttributes: []restic.Attribute{
				{"user.foo", []byte("bar")},
			},
		},
		{
			Name:       "testdirectory",
			Type:       "dir",
			Mode:       0755,
			ModTime:    parseTime("2005-05-14 21:07:03.111"),
			AccessTime: parseTime("2005-05-14 21:07:04.222"),
			ChangeTime: parseTime("2005-05-14 21:07:05.333"),
			ExtendedAttributes: []restic.Attribute{
				{"user.foo", []byte("bar")},
			},
		},
	}
	for _, testNode := range expectedNodes {
		testPath := filepath.Join(tempDir, "001", testNode.Name)
		err := os.MkdirAll(filepath.Dir(testPath), testNode.Mode)
		test.OK(t, errors.Wrapf(err, "Failed to create parent directories for: %s", testPath))

		if testNode.Type == "file" {

			testFile, err := os.Create(testPath)
			test.OK(t, errors.Wrapf(err, "Failed to create test file: %s", testPath))
			testFile.Close()
		} else if testNode.Type == "dir" {

			err := os.Mkdir(testPath, testNode.Mode)
			test.OK(t, errors.Wrapf(err, "Failed to create test directory for: %s", testPath))
		}

		err = testNode.RestoreMetadata(testPath)
		test.OK(t, errors.Wrapf(err, "Error restoring metadata for: %s", testPath))

		var handle windows.Handle
		utf16Path := windows.StringToUTF16Ptr(testPath)
		if testNode.Type == "file" {
			handle, err = windows.CreateFile(utf16Path, windows.FILE_READ_EA, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
		} else if testNode.Type == "dir" {
			handle, err = windows.CreateFile(utf16Path, windows.FILE_READ_EA, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
		}
		test.OK(t, errors.Wrapf(err, "Error opening file/directory for: %s", testPath))
		defer func() {
			err := windows.Close(handle)
			test.OK(t, errors.Wrapf(err, "Error closing file for: %s", testPath))
		}()

		if len(testNode.ExtendedAttributes) > 0 {
			extAttr, err := fs.GetFileEA(handle)
			test.OK(t, errors.Wrapf(err, "Error getting extended attributes for: %s", testPath))
			test.Equals(t, len(testNode.ExtendedAttributes), len(extAttr))

			for _, expectedExtAttr := range testNode.ExtendedAttributes {
				var foundExtAttr *fs.ExtendedAttribute
				for _, ea := range extAttr {
					if strings.EqualFold(ea.Name, expectedExtAttr.Name) {
						foundExtAttr = &ea
						break

					}
				}
				test.Assert(t, foundExtAttr != nil, "Expected extended attribute not found")
				test.Equals(t, expectedExtAttr.Value, foundExtAttr.Value)
			}
		}
	}
}

func TestRestoreSecurityDescriptors(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	expectedNodes := []restic.Node{
		{
			Name:       "testfile",
			Type:       "file",
			Mode:       0644,
			ModTime:    parseTime("2005-05-14 21:07:03.111"),
			AccessTime: parseTime("2005-05-14 21:07:04.222"),
			ChangeTime: parseTime("2005-05-14 21:07:05.333"),
			GenericAttributes: []restic.Attribute{
				restic.NewGenericAttribute(restic.TypeSecurityDescriptor, []byte("AQAUvBQAAAAwAAAAAAAAAEwAAAABBQAAAAAABRUAAACIn1iuVqCC6sy9JqvqAwAAAQUAAAAAAAUVAAAAiJ9YrlaggurMvSarAQIAAAIAfAAEAAAAAAAkAKkAEgABBQAAAAAABRUAAACIn1iuVqCC6sy9JqvtAwAAAAAUAP8BHwABAQAAAAAABRIAAAAAABgA/wEfAAECAAAAAAAFIAAAACACAAAAACQA/wEfAAEFAAAAAAAFFQAAAIifWK5WoILqzL0mq+oDAAA=")),
			},
		},
		{
			Name:       "testfile2",
			Type:       "file",
			Mode:       0644,
			ModTime:    parseTime("2005-05-14 21:07:03.111"),
			AccessTime: parseTime("2005-05-14 21:07:04.222"),
			ChangeTime: parseTime("2005-05-14 21:07:05.333"),
			GenericAttributes: []restic.Attribute{
				restic.NewGenericAttribute(restic.TypeSecurityDescriptor, []byte("AQAUvBQAAAAwAAAA7AAAAEwAAAABBQAAAAAABRUAAAAvr7t03PyHGk2FokNHCAAAAQUAAAAAAAUVAAAAiJ9YrlaggurMvSarAQIAAAIAoAAFAAAAAAAkAP8BHwABBQAAAAAABRUAAAAvr7t03PyHGk2FokNHCAAAAAAkAKkAEgABBQAAAAAABRUAAACIn1iuVqCC6sy9JqvtAwAAAAAUAP8BHwABAQAAAAAABRIAAAAAABgA/wEfAAECAAAAAAAFIAAAACACAAAAACQA/wEfAAEFAAAAAAAFFQAAAIifWK5WoILqzL0mq+oDAAACAHQAAwAAAAKAJAC/AQIAAQUAAAAAAAUVAAAAL6+7dNz8hxpNhaJDtgQAAALAJAC/AQMAAQUAAAAAAAUVAAAAL6+7dNz8hxpNhaJDPgkAAAJAJAD/AQ8AAQUAAAAAAAUVAAAAL6+7dNz8hxpNhaJDtQQAAA==")),
			},
		},
		{
			Name:       "testdirectory",
			Type:       "dir",
			Mode:       0755,
			ModTime:    parseTime("2005-05-14 21:07:03.111"),
			AccessTime: parseTime("2005-05-14 21:07:04.222"),
			ChangeTime: parseTime("2005-05-14 21:07:05.333"),
			GenericAttributes: []restic.Attribute{
				restic.NewGenericAttribute(restic.TypeSecurityDescriptor, []byte("AQAUvBQAAAAwAAAAAAAAAEwAAAABBQAAAAAABRUAAACIn1iuVqCC6sy9JqvqAwAAAQUAAAAAAAUVAAAAiJ9YrlaggurMvSarAQIAAAIAfAAEAAAAAAAkAKkAEgABBQAAAAAABRUAAACIn1iuVqCC6sy9JqvtAwAAAAMUAP8BHwABAQAAAAAABRIAAAAAAxgA/wEfAAECAAAAAAAFIAAAACACAAAAAyQA/wEfAAEFAAAAAAAFFQAAAIifWK5WoILqzL0mq+oDAAA=")),
			},
		},
		{
			Name:       "testdirectory2",
			Type:       "dir",
			Mode:       0755,
			ModTime:    parseTime("2005-05-14 21:07:03.111"),
			AccessTime: parseTime("2005-05-14 21:07:04.222"),
			ChangeTime: parseTime("2005-05-14 21:07:05.333"),
			GenericAttributes: []restic.Attribute{
				restic.NewGenericAttribute(restic.TypeSecurityDescriptor, []byte("AQAUvBQAAAAwAAAAAAAAAEwAAAABBQAAAAAABRUAAACIn1iuVqCC6sy9JqvqAwAAAQUAAAAAAAUVAAAAiJ9YrlaggurMvSarAQIAAAIA3AAIAAAAAAIUAKkAEgABAQAAAAAABQcAAAAAAxQAiQASAAEBAAAAAAAFBwAAAAAAJACpABIAAQUAAAAAAAUVAAAAiJ9YrlaggurMvSar7QMAAAAAJAC/ARMAAQUAAAAAAAUVAAAAiJ9YrlaggurMvSar6gMAAAALFAC/ARMAAQEAAAAAAAMAAAAAAAMUAP8BHwABAQAAAAAABRIAAAAAAxgA/wEfAAECAAAAAAAFIAAAACACAAAAAyQA/wEfAAEFAAAAAAAFFQAAAIifWK5WoILqzL0mq+oDAAA=")),
			},
		},
	}
	for _, testNode := range expectedNodes {
		testPath, node := restoreAndGetNode(t, tempDir, testNode)

		sd, err := fs.GetFileSecurityDescriptor(testPath)

		test.Assert(t, err == nil, "Error while getting the security descriptor")

		testSD := string(node.GetGenericAttribute(restic.TypeSecurityDescriptor))
		sdBytesTest, err := base64.StdEncoding.DecodeString(testSD)
		test.OK(t, errors.Wrapf(err, "Error decoding SD for: %s", testPath))
		sdInput, err := fs.SecurityDescriptorBytesToStruct(sdBytesTest)

		test.OK(t, errors.Wrapf(err, "Error converting SD to struct for: %s", testPath))

		sdBytesOutput, err := base64.StdEncoding.DecodeString(sd)
		test.OK(t, errors.Wrapf(err, "Error decoding SD for: %s", testPath))

		sdOutput, err := fs.SecurityDescriptorBytesToStruct(sdBytesOutput)
		test.OK(t, errors.Wrapf(err, "Error converting Output SD to struct for: %s", testPath))

		test.Equals(t, sdInput, sdOutput, "SecurityDescriptors not equal for path: %s", testPath)

		fi, err := os.Lstat(testPath)
		test.OK(t, errors.Wrapf(err, "Error running Lstat for: %s", testPath))

		nodeFromFileInfo, err := restic.NodeFromFileInfo(testPath, fi)
		test.OK(t, errors.Wrapf(err, "Error getting node from fileInfo for: %s", testPath))

		sdNodeFromFileInfoInput := sdOutput

		sdBytesFromNode := nodeFromFileInfo.GetGenericAttribute(restic.TypeSecurityDescriptor)

		sdByteNodeOutput, err := base64.StdEncoding.DecodeString(string(sdBytesFromNode))
		test.OK(t, errors.Wrapf(err, "Error decoding SD for: %s", testPath))

		sdNodeFromFileInfoOutput, err := fs.SecurityDescriptorBytesToStruct(sdByteNodeOutput)
		test.OK(t, errors.Wrapf(err, "Error converting SD Output Node to struct for: %s", testPath))

		test.Equals(t, sdNodeFromFileInfoInput, sdNodeFromFileInfoOutput, "SecurityDescriptors got from NodeFromFileInfo not equal for path: %s", testPath)
	}
}

func TestRestoreCreationTime(t *testing.T) {
	t.Parallel()
	path := t.TempDir()
	fi, err := os.Lstat(path)
	test.OK(t, errors.Wrapf(err, "Could not Lstat for path: %s", path))
	creationTimeAttribute := restic.GetCreationTime(fi, path)
	test.OK(t, errors.Wrapf(err, "Could not get creation time for path: %s", path))
	//Using the temp dir creation time as the test creation time for the test file and folder
	runGenericAttributesTest(t, path, restic.TypeCreationTime, creationTimeAttribute.Value)
}

func TestRestoreFileAttributes(t *testing.T) {
	t.Parallel()
	genericAttributeName := restic.TypeFileAttribute
	tempDir := t.TempDir()
	fileAttributes := [][]byte{
		//normal
		restic.UInt32ToBytes(syscall.FILE_ATTRIBUTE_NORMAL),
		//hidden
		restic.UInt32ToBytes(syscall.FILE_ATTRIBUTE_HIDDEN),
		//system
		restic.UInt32ToBytes(syscall.FILE_ATTRIBUTE_SYSTEM),
		//archive
		restic.UInt32ToBytes(syscall.FILE_ATTRIBUTE_ARCHIVE),
		//encrypted
		restic.UInt32ToBytes(windows.FILE_ATTRIBUTE_ENCRYPTED),
	}
	for i, fileAttr := range fileAttributes {
		expectedNodes := []restic.Node{
			{
				Name:              fmt.Sprintf("testfile%d", i),
				Type:              "file",
				Mode:              0655,
				ModTime:           parseTime("2005-05-14 21:07:03.111"),
				AccessTime:        parseTime("2005-05-14 21:07:04.222"),
				ChangeTime:        parseTime("2005-05-14 21:07:05.333"),
				GenericAttributes: []restic.Attribute{restic.NewGenericAttribute(genericAttributeName, fileAttr)},
			},
		}
		runGenericAttributesTestForNodes(t, expectedNodes, tempDir, genericAttributeName, fileAttr)
	}

	folderAttributes := [][]byte{
		//hidden
		restic.UInt32ToBytes(syscall.FILE_ATTRIBUTE_DIRECTORY | syscall.FILE_ATTRIBUTE_HIDDEN),
		//normal
		restic.UInt32ToBytes(syscall.FILE_ATTRIBUTE_DIRECTORY),
		//encrypted
		restic.UInt32ToBytes(syscall.FILE_ATTRIBUTE_DIRECTORY | windows.FILE_ATTRIBUTE_ENCRYPTED),
		//system
		restic.UInt32ToBytes(syscall.FILE_ATTRIBUTE_DIRECTORY | windows.FILE_ATTRIBUTE_SYSTEM),
		//archive
		restic.UInt32ToBytes(syscall.FILE_ATTRIBUTE_DIRECTORY | windows.FILE_ATTRIBUTE_ARCHIVE),
	}
	for i, folderAttr := range folderAttributes {
		expectedNodes := []restic.Node{
			{
				Name:              fmt.Sprintf("testdirectory%d", i),
				Type:              "dir",
				Mode:              0755,
				ModTime:           parseTime("2005-05-14 21:07:03.111"),
				AccessTime:        parseTime("2005-05-14 21:07:04.222"),
				ChangeTime:        parseTime("2005-05-14 21:07:05.333"),
				GenericAttributes: []restic.Attribute{restic.NewGenericAttribute(genericAttributeName, folderAttr)},
			},
		}
		runGenericAttributesTestForNodes(t, expectedNodes, tempDir, genericAttributeName, folderAttr)
	}
}

func runGenericAttributesTest(t *testing.T, tempDir string, genericAttributeName restic.GenericAttributeType, genericAttributeExpected []byte) {
	expectedNodes := []restic.Node{
		{
			Name:              "testfile",
			Type:              "file",
			Mode:              0644,
			ModTime:           parseTime("2005-05-14 21:07:03.111"),
			AccessTime:        parseTime("2005-05-14 21:07:04.222"),
			ChangeTime:        parseTime("2005-05-14 21:07:05.333"),
			GenericAttributes: []restic.Attribute{restic.NewGenericAttribute(genericAttributeName, genericAttributeExpected)},
		},
		{
			Name:              "testdirectory",
			Type:              "dir",
			Mode:              0755,
			ModTime:           parseTime("2005-05-14 21:07:03.111"),
			AccessTime:        parseTime("2005-05-14 21:07:04.222"),
			ChangeTime:        parseTime("2005-05-14 21:07:05.333"),
			GenericAttributes: []restic.Attribute{restic.NewGenericAttribute(genericAttributeName, genericAttributeExpected)},
		},
	}
	runGenericAttributesTestForNodes(t, expectedNodes, tempDir, genericAttributeName, genericAttributeExpected)
}
func runGenericAttributesTestForNodes(t *testing.T, expectedNodes []restic.Node, tempDir string, genericAttr restic.GenericAttributeType, genericAttributeExpected []byte) {

	for _, testNode := range expectedNodes {
		testPath, node := restoreAndGetNode(t, tempDir, testNode)
		test.Equals(t, genericAttributeExpected, node.GetGenericAttribute(genericAttr), "Generic attribute: %s got from NodeFromFileInfo not equal for path: %s", string(genericAttr), testPath)
	}
}

func restoreAndGetNode(t *testing.T, tempDir string, testNode restic.Node) (string, *restic.Node) {
	testPath := filepath.Join(tempDir, "001", testNode.Name)
	err := os.MkdirAll(filepath.Dir(testPath), testNode.Mode)
	test.OK(t, errors.Wrapf(err, "Failed to create parent directories for: %s", testPath))

	if testNode.Type == "file" {

		testFile, err := os.Create(testPath)
		test.OK(t, errors.Wrapf(err, "Failed to create test file: %s", testPath))
		testFile.Close()
	} else if testNode.Type == "dir" {

		err := os.Mkdir(testPath, testNode.Mode)
		test.OK(t, errors.Wrapf(err, "Failed to create test directory: %s", testPath))
	}

	err = testNode.RestoreMetadata(testPath)
	test.OK(t, errors.Wrapf(err, "Failed to restore metadata for: %s", testPath))

	fi, err := os.Lstat(testPath)
	test.OK(t, errors.Wrapf(err, "Could not Lstat for path: %s", testPath))

	nodeFromFileInfo, err := restic.NodeFromFileInfo(testPath, fi)
	test.OK(t, errors.Wrapf(err, "Could not get NodeFromFileInfo for path: %s", testPath))

	return testPath, nodeFromFileInfo
}

const TypeSomeNewAttribute restic.GenericAttributeType = "someNewAttribute"

func TestNewGenericAttributeType(t *testing.T) {
	t.Parallel()
	genericAttributeName := TypeSomeNewAttribute
	tempDir := t.TempDir()
	expectedNodes := []restic.Node{
		{
			Name:              "testfile",
			Type:              "file",
			Mode:              0644,
			ModTime:           parseTime("2005-05-14 21:07:03.111"),
			AccessTime:        parseTime("2005-05-14 21:07:04.222"),
			ChangeTime:        parseTime("2005-05-14 21:07:05.333"),
			GenericAttributes: []restic.Attribute{restic.NewGenericAttribute(genericAttributeName, []byte("any value"))},
		},
		{
			Name:              "testdirectory",
			Type:              "dir",
			Mode:              0755,
			ModTime:           parseTime("2005-05-14 21:07:03.111"),
			AccessTime:        parseTime("2005-05-14 21:07:04.222"),
			ChangeTime:        parseTime("2005-05-14 21:07:05.333"),
			GenericAttributes: []restic.Attribute{restic.NewGenericAttribute(genericAttributeName, []byte("any value"))},
		},
	}
	for _, testNode := range expectedNodes {
		testPath, node := restoreAndGetNode(t, tempDir, testNode)
		//Since this GenericAttribute is unknown to this version of the software, it will not get set on the file.
		test.Equals(t, []byte(nil), node.GetGenericAttribute(TypeSomeNewAttribute), "Unknown Generic attribute: %s got from NodeFromFileInfo not equal for path: %s", string(TypeSomeNewAttribute), testPath)
	}
}
