//go:build windows
// +build windows

package restic_test

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/restic/restic/internal/errors"
	"github.com/restic/restic/internal/restic"
	"github.com/restic/restic/internal/test"
)

func TestRestoreCreationTime(t *testing.T) {
	tempDir := t.TempDir()
	//Using the temp dir creation time as the test creation time for the test file and folder
	creationTime := getCreationTime(t, tempDir)
	runGenericAttributesTest(tempDir, t, restic.TypeCreationTime, creationTime)
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
		runGenericAttributesTestForNodes(expectedNodes, tempDir, t, genericAttributeName, fileAttr)
	}

	folderAttributes := [][]byte{
		//hidden
		restic.UInt32ToBytes(syscall.FILE_ATTRIBUTE_DIRECTORY | syscall.FILE_ATTRIBUTE_HIDDEN),
		//normal
		restic.UInt32ToBytes(syscall.FILE_ATTRIBUTE_DIRECTORY),
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
		runGenericAttributesTestForNodes(expectedNodes, tempDir, t, genericAttributeName, folderAttr)
	}
}

func runGenericAttributesTest(tempDir string, t *testing.T, genericAttributeName restic.GenericAttributeType, genericAttributeExpected []byte) {
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
	runGenericAttributesTestForNodes(expectedNodes, tempDir, t, genericAttributeName, genericAttributeExpected)
}
func runGenericAttributesTestForNodes(expectedNodes []restic.Node, tempDir string, t *testing.T, genericAttr restic.GenericAttributeType, genericAttributeExpected []byte) {

	for _, testNode := range expectedNodes {
		testPath, genericAttrThroughNodeFromFileInfo := prepareGenericAttributesTestForNodes(tempDir, testNode, t, genericAttr)
		test.Equals(t, genericAttributeExpected, genericAttrThroughNodeFromFileInfo, "Generic attribute: %s got from NodeFromFileInfo not equal for path: %s", string(genericAttr), testPath)
	}
}

func prepareGenericAttributesTestForNodes(tempDir string, testNode restic.Node, t *testing.T, genericAttr restic.GenericAttributeType) (string, []byte) {
	testPath := filepath.Join(tempDir, "001", testNode.Name)
	if err := os.MkdirAll(filepath.Dir(testPath), testNode.Mode); err != nil {
		t.Fatalf("Failed to create parent directories: %v", err)
	}
	if testNode.Type == "file" {

		testFile, err := os.Create(testPath)
		if err != nil {
			t.Fatalf("Failed to create test file: %v", err)
		}
		testFile.Close()
	} else if testNode.Type == "dir" {

		err := os.Mkdir(testPath, testNode.Mode)
		if err != nil {
			t.Fatalf("Failed to create test directory: %v", err)
		}
	}

	err := testNode.RestoreMetadata(testPath)
	if err != nil {
		t.Fatalf("Error restoring metadata: %v", err)
	}

	fi, err := os.Lstat(testPath)
	if err != nil {
		t.Fatal(errors.Wrapf(err, "Could not Lstat for path: %s", testPath))
	}

	nodeFromFileInfo, err := restic.NodeFromFileInfo(testPath, fi)
	if err != nil {
		t.Fatal(errors.Wrapf(err, "Could not get NodeFromFileInfo for path: %s", testPath))
	}
	genericAttrThroughNodeFromFileInfo := nodeFromFileInfo.GetGenericAttribute(genericAttr)
	return testPath, genericAttrThroughNodeFromFileInfo
}

func getCreationTime(t *testing.T, path string) []byte {
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(errors.Wrapf(err, "Could not Lstat for path: %s", path))
	}

	attrib, success := fi.Sys().(*syscall.Win32FileAttributeData)
	if success && attrib != nil {
		var creationTime [8]byte
		binary.LittleEndian.PutUint32(creationTime[0:4], attrib.CreationTime.LowDateTime)
		binary.LittleEndian.PutUint32(creationTime[4:8], attrib.CreationTime.HighDateTime)
		return creationTime[:]
	} else {
		t.Fatal("Could not get creation time for path: " + path)
	}
	return nil
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
		testPath, genericAttrThroughNodeFromFileInfo := prepareGenericAttributesTestForNodes(tempDir, testNode, t, TypeSomeNewAttribute)
		//Since this GenericAttribute is unknown to this version of the software, it will not get set on the file.
		test.Equals(t, []byte(nil), genericAttrThroughNodeFromFileInfo, "Unknown Generic attribute: %s got from NodeFromFileInfo not equal for path: %s", string(TypeSomeNewAttribute), testPath)
	}
}