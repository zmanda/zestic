package restorer

import (
	"errors"
	"os"
	"strings"
	"sync"

	"github.com/restic/restic/internal/fs"
	"github.com/restic/restic/internal/restic"
)

// OpenFile opens the file with truncate and write only options.
// We need to handle the readonly attribute and ads related logic during file creation.
// Readonly files - if an existing file is detected as readonly we clear the flag because otherwise we cannot
// make changes to the file. The readonly attribute would be set again in the second pass when the attributes
// are set if the file version being restored has the readonly bit.
// ADS files need special handling - Each stream is treated as a separate file in restic. This method is called
// for the main file which has the streams and for each stream.
// If the ads stream calls this method first and the main file doesn't already exist, the creating the file
// for the streams causes the main file to automatically get created with 0 size. Hence we need to be careful
// while creating the main file. If we blindly create it with the os.O_CREATE option, it could overwrite the
// stream. However creating the stream with os.O_CREATE option does not overwrite the mainfile if it already
// exists. It will simply attach the new stream to the main file if the main file existed, otherwise it will
// create the 0 size main file.
// Another case to handle is if the mainfile already had more streams and the file version being restored has
// less streams, then the extra streams need to be removed from the main file. The stream names are present
// as the value in the generic attribute TypeHasAds.
func (fw *filesWriter) OpenFile(createSize int64, path string, fileInfo *fileInfo) (file *os.File, err error) {
	var mainPath string
	mainPath, file, err = fw.openFileImpl(createSize, path, fileInfo)
	if err != nil && fs.IsAccessDenied(err) {
		// Access is denied, remove readonly and try again.
		// ClearReadonly removes the readonly flag from the main file
		// as it will be set again while applying metadata in the next pass if required.
		err = fs.ClearReadonly(mainPath)
		if err == nil {
			_, file, err = fw.openFileImpl(createSize, path, fileInfo)
			if err != nil {
				return nil, err
			}
		}
	}
	return file, err
}

// openFileImpl is the actual open file implementation.
func (fw *filesWriter) openFileImpl(createSize int64, path string, fileInfo *fileInfo) (mainPath string, file *os.File, err error) {
	var flags int
	if createSize >= 0 {
		// File needs to be created or replaced

		//Define all the flags
		var hasAds, isAds, isAlreadyExists bool
		var adsValues []string
		adsValues, hasAds, isAds = getAdsAttributes(fileInfo.attrs)

		// This means that this is an ads related file. It either has ads streams or is an ads streams
		isAdsRelated := hasAds || isAds

		var mainPath string
		if isAds {
			mainPath = fs.TrimAds(path)
		} else {
			mainPath = path
		}
		if isAdsRelated {
			// Get or create a mutex based on the main file path
			mutex := GetOrCreateMutex(mainPath)
			mutex.Lock()
			defer mutex.Unlock()
			// Making sure the code below doesn't execute concurrently for the main file and any of the ads files
		}

		if err != nil {
			return mainPath, nil, err
		}
		// First check if file already exists
		file, err = openFileWithTruncWrite(path)
		if err == nil {
			// File already exists
			isAlreadyExists = true
		} else if !os.IsNotExist(err) {
			// Any error other that IsNotExist error, then do not continue.
			// If the error was because access is denied,
			// the calling method will try to check if the file is readonly and if so, it tries to
			// remove the readonly attribute and call this openFileImpl method again once.
			// If this method throws access denied again, then it stops trying and return the error.
			return mainPath, nil, err
		}
		//At this point readonly flag is already handled and we need not consider it anymore.

		file, err = handleCreateFile(path, mainPath, file, adsValues, isAdsRelated, hasAds, isAds, isAlreadyExists)
	} else {
		// File is already created. For subsequent writes, only use os.O_WRONLY flag.
		flags = os.O_WRONLY
		file, err = os.OpenFile(path, flags, 0600)
	}

	return mainPath, file, err
}

// handleCreateFile handles all the various combination of states while creating the file if needed.
func handleCreateFile(path string, mainPath string, fileIn *os.File, adsValues []string, isAdsRelated, hasAds, isAds, isAlreadyExists bool) (file *os.File, err error) {
	if !isAdsRelated {
		// This is the simplest case where ADS files are not involved.
		file, err = handleCreateFileNonAds(path, fileIn, isAlreadyExists)
	} else {
		// This is a complex case needing coordination between the main file and the ads files.
		file, err = handleCreateFileAds(path, mainPath, fileIn, adsValues, hasAds, isAds, isAlreadyExists)
	}

	return file, err
}

// handleCreateFileNonAds handles all the various combination of states while creating the non-ads file if needed.
func handleCreateFileNonAds(path string, fileIn *os.File, isAlreadyExists bool) (file *os.File, err error) {
	// This is the simple case. We do not need to change the encryption attribute.
	if isAlreadyExists {
		// If the non-ads file already exists and no change to encryption, return the file
		// that we already created without create option.
		return fileIn, nil
	} else {
		// If the non-ads file did not exist, try creating the file with create flag.
		return openFileWithCreate(path)
	}
}

// handleCreateFileAds handles all the various combination of states while creating the ads related file if needed.
func handleCreateFileAds(path string, mainPath string, fileIn *os.File, adsValues []string, hasAds, isAds, isAlreadyExists bool) (file *os.File, err error) {
	// This is the simple case. We do not need to change the encryption attribute.
	if isAlreadyExists {
		// If the ads related file already exists and no change to encryption, return the file
		// that we already created without create option.
		return fileIn, nil
	} else {
		// If the ads related file did not exist, first check if it is a hasAds or isAds
		if isAds {
			// If it is an ads file, then we can simple open it with create options without worrying about overwriting.
			return openFileWithCreate(path)
		}
		if hasAds {
			// If it is the main file which has ads files attached, we will check again if the main file wasn't created
			// since we synced.
			file, err = openFileWithTruncWrite(path)
			if err != nil {
				if os.IsNotExist(err) {
					// We confirmed that the main file still doesn't exist after syncing.
					// Hence creating the file with the create flag.
					// Directly open the main file with create option as it should not be encrypted.
					return openFileWithCreate(path)
				} else {
					// Some other error occured so stop processing and return it.
					return nil, err
				}
			} else {
				// This means that the main file exists now and requires no change to encryption. Simply return it.
				return file, err
			}
		}
		return nil, errors.New("invalid case for ads same file encryption")
	}
}

// Helper methods

// openFileWithCreate opens the file with os.O_CREATE flag along with os.O_TRUNC and os.O_WRONLY.
func openFileWithCreate(path string) (file *os.File, err error) {
	flags := os.O_CREATE | os.O_TRUNC | os.O_WRONLY
	return os.OpenFile(path, flags, 0600)
}

// openFileWithCreate opens the file without os.O_CREATE flag along with os.O_TRUNC and os.O_WRONLY.
func openFileWithTruncWrite(path string) (file *os.File, err error) {
	flags := os.O_TRUNC | os.O_WRONLY
	return os.OpenFile(path, flags, 0600)
}

var pathMutexMap = PathMutexMap{
	mutex: make(map[string]*sync.Mutex),
}

// PathMutexMap represents a map of mutexes, where each path maps to a unique mutex.
type PathMutexMap struct {
	mu    sync.RWMutex
	mutex map[string]*sync.Mutex
}

// CleanupPath performs clean up for the specified path.
func CleanupPath(path string) {
	removeMutex(path)
}

// removeMutex removes the mutex for the specified path.
func removeMutex(path string) {
	path = fs.TrimAds(path)
	pathMutexMap.mu.Lock()
	defer pathMutexMap.mu.Unlock()

	// Delete the mutex from the map
	delete(pathMutexMap.mutex, path)
}

// Cleanup performs cleanup for all paths.
// It clears all the mutexes in the map.
func Cleanup() {
	pathMutexMap.mu.Lock()
	defer pathMutexMap.mu.Unlock()
	// Iterate over the map and remove each mutex
	for path, mutex := range pathMutexMap.mutex {
		// You can optionally do additional cleanup or release resources associated with the mutex
		mutex.Lock()
		// Delete the mutex from the map
		delete(pathMutexMap.mutex, path)
		mutex.Unlock()
	}
}

// GetOrCreateMutex returns the mutex associated with the given path.
// If the mutex doesn't exist, it creates a new one.
func GetOrCreateMutex(path string) *sync.Mutex {
	pathMutexMap.mu.RLock()
	mutex, ok := pathMutexMap.mutex[path]
	pathMutexMap.mu.RUnlock()

	if !ok {
		// The mutex doesn't exist, upgrade the lock and create a new one
		pathMutexMap.mu.Lock()
		defer pathMutexMap.mu.Unlock()

		// Double-check if another goroutine has created the mutex
		if mutex, ok = pathMutexMap.mutex[path]; !ok {
			mutex = &sync.Mutex{}
			pathMutexMap.mutex[path] = mutex
		}
	}

	return mutex
}

// getAdsAttributes gets all the ads related attributes.
func getAdsAttributes(attrs []restic.GenericAttribute) (adsValues []string, hasAds, isAds bool) {
	if len(attrs) > 0 {
		adsBytes := restic.GetGenericAttribute(restic.TypeHasADS, attrs)
		adsString := string(adsBytes)
		adsValues = strings.Split(adsString, restic.AdsSeparator)

		hasAds = adsBytes != nil
		isAds = restic.GetGenericAttribute(restic.TypeIsADS, attrs) != nil
	}
	return adsValues, hasAds, isAds
}
