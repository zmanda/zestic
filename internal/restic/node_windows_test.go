//go:build windows
// +build windows

package restic_test

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/restic/restic/internal/errors"
	"github.com/restic/restic/internal/restic"
	"github.com/restic/restic/internal/test"
	"golang.org/x/sys/windows"
)

func TestRestoreCreationTime(t *testing.T) {
	path := t.TempDir()
	fi, err := os.Lstat(path)
	test.OK(t, errors.Wrapf(err, "Could not Lstat for path: %s", path))
	creationTimeAttribute := restic.GetCreationTime(fi, path)
	test.OK(t, errors.Wrapf(err, "Could not get creation time for path: %s", path))
	//Using the temp dir creation time as the test creation time for the test file and folder
	runGenericAttributesTest(t, path, restic.TypeCreationTime, creationTimeAttribute.Value)
}

func TestRestoreFileAttributes(t *testing.T) {
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
		testPath, node := restoreAndGetNode(t, tempDir, testNode, genericAttr)
		test.Equals(t, genericAttributeExpected, node.GetGenericAttribute(genericAttr), "Generic attribute: %s got from NodeFromFileInfo not equal for path: %s", string(genericAttr), testPath)
	}
}

func restoreAndGetNode(t *testing.T, tempDir string, testNode restic.Node, genericAttr restic.GenericAttributeType) (string, *restic.Node) {
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
		testPath, node := restoreAndGetNode(t, tempDir, testNode, TypeSomeNewAttribute)
		//Since this GenericAttribute is unknown to this version of the software, it will not get set on the file.
		test.Equals(t, []byte(nil), node.GetGenericAttribute(TypeSomeNewAttribute), "Unknown Generic attribute: %s got from NodeFromFileInfo not equal for path: %s", string(TypeSomeNewAttribute), testPath)
	}
}
