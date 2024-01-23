//go:build windows
// +build windows

package restorer

import (
	"context"
	"math"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/restic/restic/internal/errors"
	"github.com/restic/restic/internal/fs"
	"github.com/restic/restic/internal/repository"
	"github.com/restic/restic/internal/restic"
	rtest "github.com/restic/restic/internal/test"
	"golang.org/x/sync/errgroup"
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

type AdsTestInfo struct {
	dirName     string
	fileOrder   []int
	Overwrite   bool
	IsDirectory bool
}

type NamedNode struct {
	name       string
	node       Node
	IsAds      bool
	HasAds     bool
	attributes *Attributes
}

type OrderedSnapshot struct {
	nodes []NamedNode
}

type OrderedDir struct {
	Nodes      []NamedNode
	Mode       os.FileMode
	ModTime    time.Time
	HasAds     bool
	attributes *Attributes
}

type DataStreamInfo struct {
	name      string
	data      string
	Encrypted bool
}

type AdsCombination struct {
	name               string
	newStreams         []DataStreamInfo
	existingStreams    []DataStreamInfo
	RestoreStreamFirst bool
}

type Attributes struct {
	ReadOnly  bool
	Hidden    bool
	System    bool
	Archive   bool
	Encrypted bool
}
type NodeInfo struct {
	DataStreamInfo
	parentDir   string
	attributes  Attributes
	altStreams  []DataStreamInfo
	Exists      bool
	IsDirectory bool
}

func TestOrderedAdsFile(t *testing.T) {
	dataStreams := []DataStreamInfo{
		{"mainadsfile.text", "Main file data.", false},
		{"mainadsfile.text:datastream1:$DATA", "First data stream.", false},
		{"mainadsfile.text:datastream2:$DATA", "Second data stream.", false},
	}

	var tests = map[string]AdsTestInfo{
		"main-stream-first": {
			dirName:   "dir",
			fileOrder: []int{0, 1, 2},
		},
		"second-stream-first": {
			dirName:   "dir",
			fileOrder: []int{1, 0, 2},
		},
		"main-stream-first-already-exists": {
			dirName:   "dir",
			fileOrder: []int{0, 1, 2},
			Overwrite: true,
		},
		"second-stream-first-already-exists": {
			dirName:   "dir",
			fileOrder: []int{1, 0, 2},
			Overwrite: true,
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			tempdir := rtest.TempDir(t)

			nodes := getOrderedAdsNodes(test.dirName, dataStreams[0].name, test.fileOrder, dataStreams, test.IsDirectory, nil)

			res := setup(t, nodes)

			if test.Overwrite {

				os.Mkdir(path.Join(tempdir, test.dirName), os.ModeDir)
				//Create existing files
				for _, f := range dataStreams {
					data := []byte("This is some dummy data.")

					filepath := path.Join(tempdir, test.dirName, f.name)
					// Write the data to the file
					err := os.WriteFile(path.Clean(filepath), data, 0644)
					rtest.OK(t, err)
				}
			}

			res.SelectFilter = adsConflictFilter

			err := res.RestoreTo(ctx, tempdir)
			rtest.OK(t, err)

			for _, fileIndex := range test.fileOrder {
				currentFile := dataStreams[fileIndex].name

				fp := path.Join(tempdir, test.dirName, currentFile)

				fi, err1 := os.Stat(fp)
				rtest.Assert(t, !errors.Is(err1, os.ErrNotExist), "The file "+currentFile+" does not exist")

				size := fi.Size()
				rtest.Assert(t, size > 0, "The file "+currentFile+" exists but is empty")

				content, err := os.ReadFile(fp)
				rtest.OK(t, err)
				contentString := string(content)
				rtest.Assert(t, contentString == dataStreams[fileIndex].data, "The file "+currentFile+" exists but the content is not overwritten")

			}
		})
	}
}

func TestExistingStreamRemoval(t *testing.T) {

	existingFileStreams := []DataStreamInfo{
		{"mainadsfile.text", "Existing main stream.", false},
		{"mainadsfile.text:datastream1:$DATA", "Existing stream.", false},
		{"mainadsfile.text:datastream2:$DATA", "Existing stream.", false},
		{"mainadsfile.text:datastream3:$DATA", "Existing stream.", false},
		{"mainadsfile.text:datastream4:$DATA", "Existing stream.", false},
	}

	restoringStreams := []DataStreamInfo{
		{"mainadsfile.text", "Main file data.", false},
		{"mainadsfile.text:datastream1:$DATA", "First data stream.", false},
		{"mainadsfile.text:datastream2:$DATA", "Second data stream.", false},
	}

	dirName := "dir"
	tempdir := rtest.TempDir(t)

	os.Mkdir(path.Join(tempdir, dirName), os.ModeDir)

	// Create existing files
	for _, f := range existingFileStreams {
		filepath := path.Join(tempdir, dirName, f.name)
		// Write the data to the file
		err := os.WriteFile(path.Clean(filepath), []byte(f.data), 0644)
		rtest.OK(t, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	nodes := getOrderedAdsNodes(dirName, restoringStreams[0].name, []int{0, 1, 2}, restoringStreams, false, nil)
	res := setup(t, nodes)

	res.SelectFilter = adsConflictFilter
	err := res.RestoreTo(ctx, tempdir)
	rtest.OK(t, err)

	checkExistingStreamRemoval(t, existingFileStreams, tempdir, dirName, restoringStreams)
}

func containsInRestored(target string, restoringStreams []DataStreamInfo) bool {
	for _, value := range restoringStreams {
		if value.name == target {
			return true
		}
	}
	return false
}

func checkExistingStreamRemoval(t *testing.T, existingFileStreams []DataStreamInfo, tempdir string, dirName string, restoringStreams []DataStreamInfo) {
	for i, currentFile := range existingFileStreams {
		fp := path.Join(tempdir, dirName, currentFile.name)
		fi, err1 := os.Stat(fp)

		existsInRestored := containsInRestored(currentFile.name, restoringStreams)

		if existsInRestored {
			rtest.Assert(t, !errors.Is(err1, os.ErrNotExist), "The file "+currentFile.name+" does not exist")
			rtest.OK(t, err1)
			size := fi.Size()
			rtest.Assert(t, size > 0, "The file "+currentFile.name+" exists but is empty")
			content, err := os.ReadFile(fp)
			rtest.OK(t, err)
			contentString := string(content)
			rtest.Assert(t, contentString == restoringStreams[i].data, "The file "+currentFile.name+" exists but the content is not overwritten")
		} else {
			rtest.Assert(t, errors.Is(err1, os.ErrNotExist), "The file "+currentFile.name+" should not exist")
		}

	}
}

func TestAdsDirectory(t *testing.T) {
	streams := []DataStreamInfo{
		{"dir:datastream1:$DATA", "First dir stream.", false},
		{"dir:datastream2:$DATA", "Second dir stream.", false},
	}

	var tests = map[string]AdsTestInfo{
		"not-exists": {
			dirName:     "dir",
			fileOrder:   []int{0, 1},
			IsDirectory: true,
		},
		"Overwrite": {
			dirName:     "dir",
			fileOrder:   []int{1, 0},
			Overwrite:   true,
			IsDirectory: true,
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			tempdir := rtest.TempDir(t)
			nodes := getOrderedAdsNodes(test.dirName, test.dirName, test.fileOrder, streams, test.IsDirectory, nil)
			res := setup(t, nodes)
			if test.Overwrite {
				os.Mkdir(path.Join(tempdir, test.dirName), os.ModeDir)
				if test.IsDirectory {
					os.Mkdir(path.Join(tempdir, test.dirName, test.dirName), os.ModeDir)

				}
				// Create existing files
				for _, f := range streams {
					data := []byte("This is some dummy data.")
					filepath := path.Join(tempdir, test.dirName, f.name)
					// Write the data to the file
					err := os.WriteFile(path.Clean(filepath), data, 0644)
					rtest.OK(t, err)
				}
			}
			res.SelectFilter = adsConflictFilter
			err := res.RestoreTo(ctx, tempdir)
			rtest.OK(t, err)
			for _, fileIndex := range test.fileOrder {
				currentFile := streams[fileIndex]
				fp := path.Join(tempdir, test.dirName, currentFile.name)
				fi, err1 := os.Stat(fp)
				rtest.Assert(t, !errors.Is(err1, os.ErrNotExist), "The file "+currentFile.name+" does not exist")
				rtest.OK(t, err1)
				size := fi.Size()
				rtest.Assert(t, size > 0, "The file "+currentFile.name+" exists but is empty")
				content, err := os.ReadFile(fp)
				rtest.OK(t, err)
				contentString := string(content)
				rtest.Assert(t, contentString == currentFile.data, "The file "+currentFile.name+" exists but the content is not overwritten")
			}
		})
	}
}

func generatePermutations(n int, prefix []bool) [][]bool {
	if n == 0 {
		// Return a slice containing the current permutation
		return [][]bool{append([]bool{}, prefix...)}
	}

	// Generate permutations with True
	prefixTrue := append(prefix, true)
	permsTrue := generatePermutations(n-1, prefixTrue)

	// Generate permutations with False
	prefixFalse := append(prefix, false)
	permsFalse := generatePermutations(n-1, prefixFalse)

	// Combine permutations with True and False
	return append(permsTrue, permsFalse...)
}

func getAdsCombinations() []AdsCombination {
	return []AdsCombination{
		{name: "NoAds"}, //No ads streams
		{
			name: "HasAds",
			newStreams: []DataStreamInfo{
				{"mainadsfile.text:datastream1:$DATA", "First data stream.", false},
				{"mainadsfile.text:datastream2:$DATA", "Second data stream.", false}},
		},
		{ //New ads streams
			name: "Ads-exists",
			newStreams: []DataStreamInfo{
				{"mainadsfile.text:datastream1:$DATA", "First data stream.", false},
				{"mainadsfile.text:datastream2:$DATA", "Second data stream.", false},
			},
			existingStreams: []DataStreamInfo{
				{"mainadsfile.text:datastream1:$DATA", "Old data stream.", false},
				{"mainadsfile.text:datastream2:$DATA", "Old data stream.", false},
				{"mainadsfile.text:datastream3:$DATA", "Old data stream.", false},
			},
		},
		{
			name: "HasAds-encrypted",
			newStreams: []DataStreamInfo{
				{"mainadsfile.text:datastream1:$DATA", "First data stream.", true},
				{"mainadsfile.text:datastream2:$DATA", "Second data stream.", true}},
		},
		{ //New ads streams
			name: "Ads-exists-encrypted",
			newStreams: []DataStreamInfo{
				{"mainadsfile.text:datastream1:$DATA", "First data stream.", true},
				{"mainadsfile.text:datastream2:$DATA", "Second data stream.", true},
			},
			existingStreams: []DataStreamInfo{
				{"mainadsfile.text:datastream1:$DATA", "Old data stream.", true},
				{"mainadsfile.text:datastream2:$DATA", "Old data stream.", true},
				{"mainadsfile.text:datastream3:$DATA", "Old data stream.", true},
			},
		},
	}

}

func getCombinationTestName(fi NodeInfo, ads AdsCombination, existingAttr Attributes) string {
	var fileName string = ads.name
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
func TestFileAttributeCombination(t *testing.T) {

	attributeCombinations := generatePermutations(5, []bool{})

	var adsCombinations = getAdsCombinations()

	for _, attr1 := range attributeCombinations {
		for _, ads := range adsCombinations {

			fileInfo := NodeInfo{
				DataStreamInfo: DataStreamInfo{
					name: "mainadsfile.text",
					data: "Main file data stream.",
				},
				altStreams: ads.newStreams,
				parentDir:  "dir",
				attributes: Attributes{
					ReadOnly:  attr1[0],
					Hidden:    attr1[1],
					System:    attr1[2],
					Archive:   attr1[3],
					Encrypted: attr1[4],
				},
				Exists: false,
			}

			testName := getCombinationTestName(fileInfo, ads, fileInfo.attributes)

			t.Run(testName, func(t *testing.T) {

				testDir := t.TempDir()

				res, _ := SetupWithAttributes(t, fileInfo, testDir, ads, fileInfo.attributes)

				res.SelectFilter = adsConflictFilter

				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				err := res.RestoreTo(ctx, testDir)
				rtest.OK(t, err)

				//Verify restore
				mainFilePath := path.Join(testDir, fileInfo.parentDir, fileInfo.name)
				verifyAttributes(t, mainFilePath, fileInfo.attributes)

				//Check main file restore
				verifyMainFileRestore(t, mainFilePath, fileInfo)

				//Check streams
				verifyAdsStreamRestore(t, ads, testDir, fileInfo)

			})

		}
	}
}

func TestDirAttributeCombination(t *testing.T) {

	attributeCombinations := generatePermutations(4, []bool{})

	var adsCombinations = []AdsCombination{
		{
			name: "NoAds",
		},
		{
			name: "HasAds",
			newStreams: []DataStreamInfo{
				{name: "dir:datastream1:$DATA", data: "new directory ads data"},
				{name: "dir:datastream2:$DATA", data: "new directory ads data"},
			},
		},
		{
			name: "AdsExists",
			newStreams: []DataStreamInfo{
				{name: "dir:datastream1:$DATA", data: "new directory ads data"},
				{name: "dir:datastream2:$DATA", data: "new directory ads data"},
			},
			existingStreams: []DataStreamInfo{
				{name: "dir:datastream1:$DATA", data: "old directory ads data"},
				{name: "dir:datastream2:$DATA", data: "old directory ads data"},
				{name: "dir:datastream3:$DATA", data: "old directory ads data"},
			},
		},
	}

	for _, attr1 := range attributeCombinations {
		for _, ads := range adsCombinations {

			dirInfo := NodeInfo{
				DataStreamInfo: DataStreamInfo{
					name: "dir",
				},
				altStreams: ads.newStreams,
				parentDir:  "dir",
				attributes: Attributes{
					//ReadOnly:  attr1[0], readonly not valid for directories
					Hidden:    attr1[0],
					System:    attr1[1],
					Archive:   attr1[2],
					Encrypted: attr1[3],
				},
				Exists:      false,
				IsDirectory: true,
			}

			testName := getCombinationTestName(dirInfo, ads, dirInfo.attributes)

			t.Run(testName, func(t *testing.T) {

				testDir := t.TempDir()

				res, _ := SetupWithAttributes(t, dirInfo, testDir, ads, dirInfo.attributes)

				res.SelectFilter = adsConflictFilter

				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				err := res.RestoreTo(ctx, testDir)
				rtest.OK(t, err)

				//Verify restore
				mainFilePath := path.Join(testDir, dirInfo.parentDir, dirInfo.name)
				verifyAttributes(t, mainFilePath, dirInfo.attributes)

				//Check directory exists
				_, err1 := os.Stat(mainFilePath)
				rtest.Assert(t, !errors.Is(err1, os.ErrNotExist), "The file "+dirInfo.name+" does not exist")

				//Check streams
				verifyAdsStreamRestore(t, ads, testDir, dirInfo)

			})

		}
	}
}

func TestFileAttributeCombinationsOverwrite(t *testing.T) {

	attributeCombinations := generatePermutations(5, []bool{})
	overwriteCombinations := generatePermutations(5, []bool{})

	var adsCombinations = getAdsCombinations()

	for _, attr1 := range attributeCombinations {
		for _, ads := range adsCombinations {

			fileInfo := NodeInfo{
				DataStreamInfo: DataStreamInfo{
					name: "mainadsfile.text",
					data: "Main file data stream.",
				},
				altStreams: ads.newStreams,
				parentDir:  "dir",
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

			for _, existingFileAttr := range existingFileAttribute {
				testName := getCombinationTestName(fileInfo, ads, existingFileAttr)

				t.Run(testName, func(t *testing.T) {

					testDir := t.TempDir()

					res, _ := SetupWithAttributes(t, fileInfo, testDir, ads, existingFileAttr)

					res.SelectFilter = adsConflictFilter

					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()

					err := res.RestoreTo(ctx, testDir)
					rtest.OK(t, err)

					//Verify restore
					mainFilePath := path.Join(testDir, fileInfo.parentDir, fileInfo.name)
					verifyAttributes(t, mainFilePath, fileInfo.attributes)

					//Check main file restore
					verifyMainFileRestore(t, mainFilePath, fileInfo)

					//Check streams
					verifyAdsStreamRestore(t, ads, testDir, fileInfo)

				})
			}
		}
	}
}

func verifyAdsStreamRestore(t *testing.T, ads AdsCombination, testDir string, fileInfo NodeInfo) {
	if len(ads.existingStreams) != 0 {
		checkExistingStreamRemoval(t, ads.existingStreams, testDir, fileInfo.parentDir, ads.newStreams)
	} else if len(ads.newStreams) != 0 {
		for _, stream := range ads.newStreams {
			currentFile := stream.name

			fp := path.Join(testDir, fileInfo.parentDir, currentFile)

			fi, err1 := os.Stat(fp)
			rtest.Assert(t, !errors.Is(err1, os.ErrNotExist), "The file "+currentFile+" does not exist")

			size := fi.Size()
			rtest.Assert(t, size > 0, "The file "+currentFile+" exists but is empty")

			content, err := os.ReadFile(fp)
			rtest.OK(t, err)
			rtest.Assert(t, string(content) == stream.data, "The file "+currentFile+" exists but the content is not overwritten")

		}
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

func verifyAttributes(t *testing.T, mainFilePath string, attr Attributes) {
	ptr, err := windows.UTF16PtrFromString(mainFilePath)
	rtest.OK(t, err)

	fileAttributes, err := syscall.GetFileAttributes(ptr)
	rtest.OK(t, err)

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

func createEncryptedFileWriteData(filepath string, fileInfo NodeInfo) error {
	var ptr *uint16
	ptr, err := windows.UTF16PtrFromString(filepath)
	if err == nil {
		var handle windows.Handle
		handle, err = windows.CreateFile(ptr, uint32(windows.GENERIC_READ|windows.GENERIC_WRITE), uint32(windows.FILE_SHARE_READ), nil, uint32(windows.CREATE_ALWAYS), windows.FILE_ATTRIBUTE_ENCRYPTED, 0)

		if err != nil {
			return err
		}

		_, err = windows.Write(handle, []byte(fileInfo.data))
		if err != nil {
			return err
		}

		err = windows.CloseHandle(handle)
		if err != nil {
			return err
		}
	}

	return err
}

func SetupWithAttributes(t *testing.T, nodeInfo NodeInfo, testDir string, ads AdsCombination, existingFileAttr Attributes) (*Restorer, []int) {

	if nodeInfo.Exists || len(ads.existingStreams) > 0 {
		if !nodeInfo.IsDirectory {
			os.MkdirAll(path.Join(testDir, nodeInfo.parentDir), os.ModeDir)

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
			os.MkdirAll(path.Join(testDir, nodeInfo.parentDir, nodeInfo.name), os.ModeDir)
		}
	}

	if len(ads.existingStreams) > 0 {
		// Create existing files
		for _, f := range ads.existingStreams {
			filepath := path.Join(testDir, nodeInfo.parentDir, f.name)
			// Write the data to the file
			file, err := os.OpenFile(path.Clean(filepath), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
			rtest.OK(t, err)
			_, err = file.Write([]byte(f.data))
			rtest.OK(t, err)

			err = file.Close()
			rtest.OK(t, err)

		}

	}

	if nodeInfo.Exists || len(ads.existingStreams) > 0 {
		pathPointer, err := syscall.UTF16PtrFromString(path.Join(testDir, nodeInfo.parentDir, nodeInfo.name))
		rtest.OK(t, err)
		syscall.SetFileAttributes(pathPointer, GetAttributeValue(&existingFileAttr))
	}

	index := 0
	order := []int{} //Need for no ads tests

	if !nodeInfo.IsDirectory {
		order = append(order, index) //For file ads we need to consider the mainfile also in the order of stream for the tests to work
		index++
	}

	for i := 0; i < len(ads.newStreams); i++ {
		order = append(order, i+index)
	}

	if ads.RestoreStreamFirst {
		//We want to restore a stream first to check if the stream get deleted on main file restore
		//Here we are swapping the main stream and stream indexes

		order[0] = 1
		order[1] = 2
	}

	streams := []DataStreamInfo{}

	if !nodeInfo.IsDirectory {
		streams = append(streams, nodeInfo.DataStreamInfo)
	}
	streams = append(streams, ads.newStreams...)
	return setup(t, getOrderedAdsNodes(nodeInfo.parentDir, nodeInfo.name, order, streams, nodeInfo.IsDirectory, &nodeInfo.attributes)), order
}

func getOrderedAdsNodes(dir string, mainNodeName string, order []int, streams []DataStreamInfo, isDirectory bool, attributes *Attributes) []NamedNode {
	var mode os.FileMode
	if isDirectory {
		mode = os.FileMode(2147484159)
	} else {
		if attributes != nil && attributes.ReadOnly {
			mode = os.FileMode(292)
		} else {
			mode = os.FileMode(438)
		}
	}

	getFileNodes := func() []NamedNode {
		nodes := []NamedNode{}

		if isDirectory {
			nodes = append(nodes, NamedNode{
				name: mainNodeName,
				node: OrderedDir{
					ModTime:    time.Now(),
					HasAds:     len(streams) > 1,
					attributes: attributes,
					Mode:       mode,
				},
				HasAds:     len(streams) > 1,
				attributes: attributes,
			})
		}

		if len(streams) > 0 {
			for _, index := range order {
				stream := streams[index]
				hasAds := mainNodeName == stream.name && len(streams) > 1
				isAds := mainNodeName != stream.name && len(streams) > 1

				var attr *Attributes = nil
				if mainNodeName == stream.name {
					attr = attributes
				} else if attributes != nil && attributes.Encrypted {
					attr = &Attributes{Encrypted: true}
				}

				nodes = append(nodes, NamedNode{
					name: stream.name,
					node: File{
						ModTime: time.Now(),
						Data:    stream.data,
						Mode:    mode,
					},
					HasAds:     hasAds,
					IsAds:      isAds,
					attributes: attr,
				})
			}
		}

		return nodes
	}

	return []NamedNode{
		{
			name: dir,
			node: OrderedDir{
				Mode:    normalizeFileMode(0750 | mode),
				ModTime: time.Now(),
				Nodes:   getFileNodes(),
			},
		},
	}
}

func adsConflictFilter(item string, dstpath string, node *restic.Node) (selectedForRestore bool, childMayBeSelected bool) {
	switch filepath.ToSlash(item) {
	case "/dir":
		childMayBeSelected = true
	case "/dir/mainadsfile.text":
		selectedForRestore = true
		childMayBeSelected = false
	case "/dir/mainadsfile.text:datastream1:$DATA":
		selectedForRestore = true
		childMayBeSelected = false
	case "/dir/mainadsfile.text:datastream2:$DATA":
		selectedForRestore = true
		childMayBeSelected = false
	case "/dir/mainadsfile.text:datastream3:$DATA":
		selectedForRestore = true
		childMayBeSelected = false
	case "/dir/mainadsfile.text:datastream4:$DATA":
		selectedForRestore = true
		childMayBeSelected = false
	case "/dir/dir":
		selectedForRestore = true
		childMayBeSelected = true
	case "/dir/dir:datastream1:$DATA":
		selectedForRestore = true
		childMayBeSelected = false
	case "/dir/dir:datastream2:$DATA":
		selectedForRestore = true
		childMayBeSelected = false
	}
	return selectedForRestore, childMayBeSelected
}

func setup(t *testing.T, namedNodes []NamedNode) *Restorer {

	repo := repository.TestRepository(t)

	sn, _ := saveOrderedSnapshot(t, repo, OrderedSnapshot{
		nodes: namedNodes,
	})

	res := NewRestorer(repo, sn, false, nil)

	return res
}

func saveDirOrdered(t testing.TB, repo restic.Repository, namedNodes []NamedNode, inode uint64) restic.ID {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	getAdsAttributes := func(path string, hasAds, isAds bool) restic.GenericAttribute {

		if isAds {
			return restic.NewGenericAttribute(restic.TypeIsADS, []byte(fs.TrimAds(path)))
		} else if hasAds {
			adsNames := []string{}

			for _, a := range namedNodes {
				if fs.TrimAds(a.name) == path {
					adsNames = append(adsNames, strings.Replace(a.name, path, "", -1))
				}
			}
			return restic.NewGenericAttribute(restic.TypeHasADS, []byte(strings.Join(adsNames, restic.AdsSeparator)))
		} else {
			return restic.GenericAttribute{}
		}
	}

	getFileAttributes := func(attr *Attributes, isDir bool) (resticAttrib restic.GenericAttribute) {

		if attr == nil {
			return
		}

		fileattr := GetAttributeValue(attr)

		if isDir {
			fileattr |= windows.FILE_ATTRIBUTE_DIRECTORY
		}

		fileAttrData := restic.UInt32ToBytes(fileattr)
		resticAttrib = restic.NewGenericAttribute(
			restic.TypeFileAttribute,
			fileAttrData,
		)

		return
	}

	tree := &restic.Tree{}
	for _, namedNode := range namedNodes {
		name := namedNode.name
		n := namedNode.node
		inode++
		switch node := n.(type) {
		case File:
			fi := n.(File).Inode
			if fi == 0 {
				fi = inode
			}
			lc := n.(File).Links
			if lc == 0 {
				lc = 1
			}
			fc := []restic.ID{}
			if len(n.(File).Data) > 0 {
				fc = append(fc, saveFile(t, repo, node))
			}
			mode := node.Mode

			genericAttributes := []restic.GenericAttribute{}
			if namedNode.attributes != nil {
				genericAttributes = append(genericAttributes, getFileAttributes(namedNode.attributes, false))
			}

			if namedNode.IsAds || namedNode.HasAds {
				genericAttributes = append(genericAttributes, getAdsAttributes(name, namedNode.HasAds, namedNode.IsAds))
			}

			err := tree.Insert(&restic.Node{
				Type:              "file",
				Mode:              mode,
				ModTime:           node.ModTime,
				Name:              name,
				UID:               uint32(os.Getuid()),
				GID:               uint32(os.Getgid()),
				Content:           fc,
				Size:              uint64(len(n.(File).Data)),
				Inode:             fi,
				Links:             lc,
				GenericAttributes: genericAttributes,
			})
			rtest.OK(t, err)
		case Dir:
			id := saveDir(t, repo, node.Nodes, inode)

			mode := node.Mode
			if mode == 0 {
				mode = 0755
			}

			err := tree.Insert(&restic.Node{
				Type:    "dir",
				Mode:    mode,
				ModTime: node.ModTime,
				Name:    name,
				UID:     uint32(os.Getuid()),
				GID:     uint32(os.Getgid()),
				Subtree: &id,
			})
			rtest.OK(t, err)
		case OrderedDir:
			id := saveDirOrdered(t, repo, node.Nodes, inode)

			mode := node.Mode

			genericAttributes := []restic.GenericAttribute{}
			if node.attributes != nil {
				genericAttributes = append(genericAttributes, getFileAttributes(node.attributes, true))
			}

			if node.HasAds {
				genericAttributes = append(genericAttributes, getAdsAttributes(name, node.HasAds, false))
			}

			err := tree.Insert(&restic.Node{
				Type:              "dir",
				Mode:              mode,
				ModTime:           node.ModTime,
				Name:              name,
				UID:               uint32(os.Getuid()),
				GID:               uint32(os.Getgid()),
				Subtree:           &id,
				GenericAttributes: genericAttributes,
			})
			rtest.OK(t, err)
		default:
			t.Fatalf("unknown node type %T", node)
		}
	}

	id, err := restic.SaveTree(ctx, repo, tree)
	if err != nil {
		t.Fatal(err)
	}

	return id
}

func GetAttributeValue(attr *Attributes) uint32 {
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

func GetNonReadonlyAttributes(attr *Attributes) uint32 {
	var fileattr uint32
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

func saveOrderedSnapshot(t testing.TB, repo restic.Repository, snapshot OrderedSnapshot) (*restic.Snapshot, restic.ID) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wg, wgCtx := errgroup.WithContext(ctx)
	repo.StartPackUploader(wgCtx, wg)
	treeID := saveDirOrdered(t, repo, snapshot.nodes, 1000)
	err := repo.Flush(ctx)
	if err != nil {
		t.Fatal(err)
	}

	sn, err := restic.NewSnapshot([]string{"test"}, nil, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	sn.Tree = &treeID
	id, err := restic.SaveSnapshot(ctx, repo, sn)
	if err != nil {
		t.Fatal(err)
	}

	return sn, id
}
