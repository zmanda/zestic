//go:build windows
// +build windows

package restorer

import (
	"context"
	"math"
	"os"
	"path"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/restic/restic/internal/errors"
	"github.com/restic/restic/internal/repository"
	"github.com/restic/restic/internal/restic"
	rtest "github.com/restic/restic/internal/test"
	"golang.org/x/sys/windows"
)

func getBlockCount(t *testing.T, filename string) int64 {
	libkernel32 := windows.NewLazySystemDLL("kernel32.dll")
	err := libkernel32.Load()
	rtest.OK(t, err)
	proc := libkernel32.NewProc("GetCompressedFileSizeW")
	err = proc.Find()
	rtest.OK(t, err)

	namePtr, err := syscall.UTF16PtrFromString(filename)
	rtest.OK(t, err)

	result, _, _ := proc.Call(uintptr(unsafe.Pointer(namePtr)), 0)

	const invalidFileSize = uintptr(4294967295)
	if result == invalidFileSize {
		return -1
	}

	return int64(math.Ceil(float64(result) / 512))
}

type Attributes struct {
	ReadOnly  bool
	Hidden    bool
	System    bool
	Archive   bool
	Encrypted bool
}
type DataStreamInfo struct {
	name string
	data string
}
type NodeInfo struct {
	DataStreamInfo
	parentDir   string
	attributes  Attributes
	Exists      bool
	IsDirectory bool
}

func TestFileAttributeCombination(t *testing.T) {
	t.Parallel()
	//Generate combination of 5 attributes.
	attributeCombinations := generateCombinations(5, []bool{})

	fileName := "TestFile.txt"
	// Iterate through each attribute combination
	for _, attr1 := range attributeCombinations {

		//Set up the required file information
		fileInfo := NodeInfo{
			DataStreamInfo: DataStreamInfo{
				name: fileName,
				data: "Main file data stream.",
			},
			parentDir: "dir",
			attributes: Attributes{
				ReadOnly:  attr1[0],
				Hidden:    attr1[1],
				System:    attr1[2],
				Archive:   attr1[3],
				Encrypted: attr1[4],
			},
			Exists: false,
		}

		//Get the current test name
		testName := getCombinationTestName(fileInfo, fileName, fileInfo.attributes)

		//Run test
		t.Run(testName, func(t *testing.T) {
			mainFilePath := runAttributeTests(t, fileInfo, fileInfo.attributes)

			//Check main file restore
			verifyMainFileRestore(t, mainFilePath, fileInfo)
		})
	}
}

func generateCombinations(n int, prefix []bool) [][]bool {
	if n == 0 {
		// Return a slice containing the current permutation
		return [][]bool{append([]bool{}, prefix...)}
	}

	// Generate combinations with True
	prefixTrue := append(prefix, true)
	permsTrue := generateCombinations(n-1, prefixTrue)

	// Generate combinations with False
	prefixFalse := append(prefix, false)
	permsFalse := generateCombinations(n-1, prefixFalse)

	// Combine combinations with True and False
	return append(permsTrue, permsFalse...)
}

func getCombinationTestName(fi NodeInfo, fileName string, existingAttr Attributes) string {
	if fi.attributes.ReadOnly {
		fileName += "-ReadOnly"
	}
	if fi.attributes.Hidden {
		fileName += "-Hidden"
	}
	if fi.attributes.System {
		fileName += "-System"
	}
	if fi.attributes.Archive {
		fileName += "-Archive"
	}
	if fi.attributes.Encrypted {
		fileName += "-Encrypted"
	}
	if fi.Exists {
		fileName += "-Overwrite"
		if existingAttr.ReadOnly {
			fileName += "-R"
		}
		if existingAttr.Hidden {
			fileName += "-H"
		}
		if existingAttr.System {
			fileName += "-S"
		}
		if existingAttr.Archive {
			fileName += "-A"
		}
		if existingAttr.Encrypted {
			fileName += "-E"
		}
	}
	return fileName
}

func runAttributeTests(t *testing.T, fileInfo NodeInfo, existingFileAttr Attributes) string {
	testDir := t.TempDir()
	res, _ := setupWithAttributes(t, fileInfo, testDir, existingFileAttr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := res.RestoreTo(ctx, testDir)
	rtest.OK(t, err)

	mainFilePath := path.Join(testDir, fileInfo.parentDir, fileInfo.name)
	//Verify restore
	verifyAttributes(t, mainFilePath, fileInfo.attributes)
	return mainFilePath
}

func setupWithAttributes(t *testing.T, nodeInfo NodeInfo, testDir string, existingFileAttr Attributes) (*Restorer, []int) {
	t.Helper()
	if nodeInfo.Exists {
		if !nodeInfo.IsDirectory {
			err := os.MkdirAll(path.Join(testDir, nodeInfo.parentDir), os.ModeDir)
			rtest.OK(t, err)
			filepath := path.Join(testDir, nodeInfo.parentDir, nodeInfo.name)
			if existingFileAttr.Encrypted {
				err := createEncryptedFileWriteData(filepath, nodeInfo)
				rtest.OK(t, err)
			} else {
				// Write the data to the file
				file, err := os.OpenFile(path.Clean(filepath), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
				rtest.OK(t, err)
				_, err = file.Write([]byte(nodeInfo.data))
				rtest.OK(t, err)

				err = file.Close()
				rtest.OK(t, err)
			}
		} else {
			err := os.MkdirAll(path.Join(testDir, nodeInfo.parentDir, nodeInfo.name), os.ModeDir)
			rtest.OK(t, err)
		}

		pathPointer, err := syscall.UTF16PtrFromString(path.Join(testDir, nodeInfo.parentDir, nodeInfo.name))
		rtest.OK(t, err)
		syscall.SetFileAttributes(pathPointer, getAttributeValue(&existingFileAttr))
	}

	index := 0
	order := []int{}

	if !nodeInfo.IsDirectory {
		order = append(order, index)
		index++
	}

	streams := []DataStreamInfo{}
	if !nodeInfo.IsDirectory {
		streams = append(streams, nodeInfo.DataStreamInfo)
	}
	return setup(t, getNodes(nodeInfo.parentDir, nodeInfo.name, order, streams, nodeInfo.IsDirectory, &nodeInfo.attributes)), order
}

func createEncryptedFileWriteData(filepath string, fileInfo NodeInfo) error {
	var ptr *uint16
	ptr, err := windows.UTF16PtrFromString(filepath)
	if err == nil {
		var handle windows.Handle
		//Create the file with encrypted flag
		handle, err = windows.CreateFile(ptr, uint32(windows.GENERIC_READ|windows.GENERIC_WRITE), uint32(windows.FILE_SHARE_READ), nil, uint32(windows.CREATE_ALWAYS), windows.FILE_ATTRIBUTE_ENCRYPTED, 0)
		if err != nil {
			return err
		}
		//Write data to file
		_, err = windows.Write(handle, []byte(fileInfo.data))
		if err != nil {
			return err
		}
		//Close handle
		err = windows.CloseHandle(handle)
		if err != nil {
			return err
		}
	}
	return err
}

func setup(t *testing.T, nodesMap map[string]Node) *Restorer {
	repo := repository.TestRepository(t)
	getFileAttributes := func(attr *Attributes, isDir bool) (genericAttributes []restic.Attribute) {
		if attr == nil {
			return
		}

		fileattr := getAttributeValue(attr)

		if isDir {
			//If the node is a directory add FILE_ATTRIBUTE_DIRECTORY to attributes
			fileattr |= windows.FILE_ATTRIBUTE_DIRECTORY
		}

		fileAttrData := restic.UInt32ToBytes(fileattr)
		//Create file attribute generic attribute
		attrib := restic.NewGenericAttribute(
			restic.TypeFileAttribute,
			fileAttrData,
		)

		//Add file attribute
		return append(genericAttributes, attrib)
	}
	sn, _ := saveSnapshot(t, repo, Snapshot{
		Nodes: nodesMap,
	}, getFileAttributes)
	res := NewRestorer(repo, sn, false, nil)
	return res
}

func getAttributeValue(attr *Attributes) uint32 {
	var fileattr uint32
	if attr.ReadOnly {
		fileattr |= windows.FILE_ATTRIBUTE_READONLY
	}
	if attr.Hidden {
		fileattr |= windows.FILE_ATTRIBUTE_HIDDEN
	}
	if attr.Encrypted {
		fileattr |= windows.FILE_ATTRIBUTE_ENCRYPTED
	}
	if attr.Archive {
		fileattr |= windows.FILE_ATTRIBUTE_ARCHIVE
	}
	if attr.System {
		fileattr |= windows.FILE_ATTRIBUTE_SYSTEM
	}
	return fileattr
}

func getNodes(dir string, mainNodeName string, order []int, streams []DataStreamInfo, isDirectory bool, attributes *Attributes) map[string]Node {
	var mode os.FileMode
	if isDirectory {
		mode = os.FileMode(2147484159)
	} else {
		if attributes != nil && attributes.ReadOnly {
			mode = os.FileMode(0o444)
		} else {
			mode = os.FileMode(0o666)
		}
	}

	getFileNodes := func() map[string]Node {
		nodes := map[string]Node{}
		if isDirectory {
			//Add a directory node at the same level as the other streams
			nodes[mainNodeName] = Dir{
				ModTime:    time.Now(),
				attributes: attributes,
				Mode:       mode,
			}
		}

		if len(streams) > 0 {
			for _, index := range order {
				stream := streams[index]

				var attr *Attributes = nil
				if mainNodeName == stream.name {
					attr = attributes
				} else if attributes != nil && attributes.Encrypted {
					//Set encrypted attribute
					attr = &Attributes{Encrypted: true}
				}

				nodes[stream.name] = File{
					ModTime:    time.Now(),
					Data:       stream.data,
					Mode:       mode,
					attributes: attr,
				}
			}
		}
		return nodes
	}

	return map[string]Node{
		dir: Dir{
			Mode:    normalizeFileMode(0750 | mode),
			ModTime: time.Now(),
			Nodes:   getFileNodes(),
		},
	}
}

func verifyAttributes(t *testing.T, mainFilePath string, attr Attributes) {
	ptr, err := windows.UTF16PtrFromString(mainFilePath)
	rtest.OK(t, err)
	//Get file attributes using syscall
	fileAttributes, err := syscall.GetFileAttributes(ptr)
	rtest.OK(t, err)
	//Test positive and negative scenarios
	if attr.ReadOnly {
		rtest.Assert(t, fileAttributes&windows.FILE_ATTRIBUTE_READONLY != 0, "Expected read only attibute.")
	} else {
		rtest.Assert(t, fileAttributes&windows.FILE_ATTRIBUTE_READONLY == 0, "Unexpected read only attibute.")
	}
	if attr.Hidden {
		rtest.Assert(t, fileAttributes&windows.FILE_ATTRIBUTE_HIDDEN != 0, "Expected hidden attibute.")
	} else {
		rtest.Assert(t, fileAttributes&windows.FILE_ATTRIBUTE_HIDDEN == 0, "Unexpected hidden attibute.")
	}
	if attr.System {
		rtest.Assert(t, fileAttributes&windows.FILE_ATTRIBUTE_SYSTEM != 0, "Expected system attibute.")
	} else {
		rtest.Assert(t, fileAttributes&windows.FILE_ATTRIBUTE_SYSTEM == 0, "Unexpected system attibute.")
	}
	if attr.Archive {
		rtest.Assert(t, fileAttributes&windows.FILE_ATTRIBUTE_ARCHIVE != 0, "Expected archive attibute.")
	} else {
		rtest.Assert(t, fileAttributes&windows.FILE_ATTRIBUTE_ARCHIVE == 0, "Unexpected archive attibute.")
	}
	if attr.Encrypted {
		rtest.Assert(t, fileAttributes&windows.FILE_ATTRIBUTE_ENCRYPTED != 0, "Expected encrypted attibute.")
	} else {
		rtest.Assert(t, fileAttributes&windows.FILE_ATTRIBUTE_ENCRYPTED == 0, "Unexpected encrypted attibute.")
	}
}

func verifyMainFileRestore(t *testing.T, mainFilePath string, fileInfo NodeInfo) {
	fi, err1 := os.Stat(mainFilePath)
	rtest.Assert(t, !errors.Is(err1, os.ErrNotExist), "The file "+fileInfo.name+" does not exist")

	size := fi.Size()
	rtest.Assert(t, size > 0, "The file "+fileInfo.name+" exists but is empty")

	content, err := os.ReadFile(mainFilePath)
	rtest.OK(t, err)
	rtest.Assert(t, string(content) == fileInfo.data, "The file "+fileInfo.name+" exists but the content is not overwritten")
}

func TestDirAttributeCombination(t *testing.T) {
	t.Parallel()
	attributeCombinations := generateCombinations(4, []bool{})

	dirName := "TestDir"
	// Iterate through each attribute combination
	for _, attr1 := range attributeCombinations {

		//Set up the required directory information
		dirInfo := NodeInfo{
			DataStreamInfo: DataStreamInfo{
				name: dirName,
			},
			parentDir: "dir",
			attributes: Attributes{
				// readonly not valid for directories
				Hidden:    attr1[0],
				System:    attr1[1],
				Archive:   attr1[2],
				Encrypted: attr1[3],
			},
			Exists:      false,
			IsDirectory: true,
		}

		//Get the current test name
		testName := getCombinationTestName(dirInfo, dirName, dirInfo.attributes)

		//Run test
		t.Run(testName, func(t *testing.T) {
			mainFilePath := runAttributeTests(t, dirInfo, dirInfo.attributes)

			//Check directory exists
			_, err1 := os.Stat(mainFilePath)
			rtest.Assert(t, !errors.Is(err1, os.ErrNotExist), "The directory "+dirInfo.name+" does not exist")
		})
	}
}

func TestFileAttributeCombinationsOverwrite(t *testing.T) {
	t.Parallel()
	//Get attribute combinations
	attributeCombinations := generateCombinations(5, []bool{})
	//Get existing file attribute combinations
	overwriteCombinations := generateCombinations(5, []bool{})

	fileName := "TestOverwriteFile"

	//Iterate through each attribute combination
	for _, attr1 := range attributeCombinations {

		fileInfo := NodeInfo{
			DataStreamInfo: DataStreamInfo{
				name: fileName,
				data: "Main file data stream.",
			},
			parentDir: "dir",
			attributes: Attributes{
				ReadOnly:  attr1[0],
				Hidden:    attr1[1],
				System:    attr1[2],
				Archive:   attr1[3],
				Encrypted: attr1[4],
			},
			Exists: true,
		}

		existingFileAttribute := []Attributes{}

		for _, overwrite := range overwriteCombinations {
			existingFileAttribute = append(existingFileAttribute, Attributes{
				ReadOnly:  overwrite[0],
				Hidden:    overwrite[1],
				Encrypted: overwrite[2],
				System:    overwrite[3],
				Archive:   overwrite[4],
			})
		}

		//Iterate through each existing attribute combination
		for _, existingFileAttr := range existingFileAttribute {
			//Get the test name
			testName := getCombinationTestName(fileInfo, fileName, existingFileAttr)

			//Run test
			t.Run(testName, func(t *testing.T) {
				mainFilePath := runAttributeTests(t, fileInfo, existingFileAttr)

				//Check main file restore
				verifyMainFileRestore(t, mainFilePath, fileInfo)
			})
		}
	}
}
