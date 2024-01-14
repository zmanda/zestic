package restorer

import (
	"encoding/binary"
	"errors"
	"os"
	"strings"
	"sync"
	"syscall"

	"github.com/restic/restic/internal/debug"
	"github.com/restic/restic/internal/fs"
	"github.com/restic/restic/internal/restic"
	"golang.org/x/sys/windows"
)

// OpenFile opens the file with truncate and write only options.
// We need to handle the encryption attribute, readonly attribute and ads related logic during file creation.
// Encryption attribute can only be set during file creation time. Changing the encryption attribute after
// file creation has no effect.
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
// We need to gracefully handle all combinations of the above attributes, starting with simpler cases.
func (fw *filesWriter) OpenFile(createSize int64, path string, fileInfo *fileInfo) (file *os.File, err error) {
	var isAds bool
	isAds, file, err = fw.openFileImpl(createSize, path, fileInfo)
	if err != nil && isAccessDenied(err) {
		// Access is denied, remove readonly and try again.
		err = clearReadonly(isAds, path, file)
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
func (fw *filesWriter) openFileImpl(createSize int64, path string, fileInfo *fileInfo) (isAds bool, file *os.File, err error) {
	var flags int
	if createSize >= 0 {
		// File needs to be created or replaced

		//Define all the flags
		var hasAds, isEncryptionNeeded, isAlreadyEncrypted, isAlreadyExists bool
		var adsValues []string
		adsValues, hasAds, isAds, isEncryptionNeeded = checkGenericAttributes(fileInfo, path)

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
			return isAds, nil, err
		}
		// First check if file already exists
		file, err = openFileWithoutCreate(path)
		if err == nil {
			// File already exists
			isAlreadyExists = true
			// File is already encrypted
			isAlreadyEncrypted, err = isFileEncrypted(file)
			if err != nil {
				// Error while checking if the file is encrypted. Close the file and return the error.
				_ = file.Close()
				return isAds, nil, err
			}
		} else if !os.IsNotExist(err) {
			// Any error other that IsNotExist error, then do not continue.
			// If the error was because access is denied,
			// the calling method will try to check if the file is readonly and if so, it tries to
			// remove the readonly attribute and call this openFileImpl method again once.
			// If this method throws access denied again, then it stops trying and return the error.
			return isAds, nil, err
		}
		//At this point readonly flag is already handled and we need not consider it anymore.

		file, err = handleCreateFile(path, mainPath, file, adsValues, isAdsRelated, hasAds, isAds, isEncryptionNeeded, isAlreadyEncrypted, isAlreadyExists)
	} else {
		// File is already created. For subsequent writes, only use os.O_WRONLY flag.
		flags = os.O_WRONLY
		file, err = os.OpenFile(path, flags, 0600)
	}

	return isAds, file, err
}

// handleCreateFile handles all the various combination of states while creating the file if needed
func handleCreateFile(path string, mainPath string, fileIn *os.File, adsValues []string, isAdsRelated, hasAds, isAds, isEncryptionNeeded, isAlreadyEncrypted, isAlreadyExists bool) (file *os.File, err error) {
	if !isAdsRelated {
		// This is the simplest case where ADS files are not involved.
		file, err = handleCreateFileNonAds(path, fileIn, isEncryptionNeeded, isAlreadyEncrypted, isAlreadyExists)
	} else {
		// This is a complex case needing coordination between the main file and the ads files.
		file, err = handleCreateFileAds(path, mainPath, fileIn, adsValues, hasAds, isAds, isEncryptionNeeded, isAlreadyEncrypted, isAlreadyExists)
	}

	return file, err
}

// handleCreateFileNonAds handles all the various combination of states while creating the non-ads file if needed
func handleCreateFileNonAds(path string, fileIn *os.File, isEncryptionNeeded, isAlreadyEncrypted, isAlreadyExists bool) (file *os.File, err error) {
	// Deduce the secondary encryption handling flags
	isSameEncryption, isAddEncryption, isRemoveEncryption := deduceEncryptionHandlingFlags(isAlreadyExists, isEncryptionNeeded, isAlreadyEncrypted)

	if isSameEncryption {
		return handleCreateFileNonAdsSameEncryption(path, fileIn, isAlreadyExists)
	}

	if isAddEncryption {
		return handleCreateFileNonAdsAddEncryption(path, fileIn, isAlreadyExists)
	}

	if isRemoveEncryption {
		return handleCreateFileNonAdsRemoveEncryption(path, fileIn, isAlreadyExists)
	}
	return nil, errors.New("invalid case for create file non-ads")
}

// handleCreateFileNonAdsSameEncryption handles creation of non ads files where there is no change in encryption attribute
func handleCreateFileNonAdsSameEncryption(path string, fileIn *os.File, isAlreadyExists bool) (file *os.File, err error) {
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

// handleCreateFileNonAdsSameEncryption handles creation of non ads files where the encryption attribute needs to be added.
func handleCreateFileNonAdsAddEncryption(path string, fileIn *os.File, isAlreadyExists bool) (file *os.File, err error) {
	// Encryption needs to be added
	if isAlreadyExists {
		// First close the already existing file which was created.
		err = fileIn.Close()
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		// File already exists, so we need to remove it before adding the new file with encryption.
		err = removeAndCreateEncryptedFile(path)
		if err != nil {
			return nil, err
		}
		return openFileWithoutCreate(path)
	} else {
		// File doesn't already exist, so we can simply create it with the encryption attribute.
		err = createEncryptedFile(path)
		if err != nil {
			return nil, err
		}
		return openFileWithoutCreate(path)
	}
}

// handleCreateFileNonAdsSameEncryption handles creation of non ads files where the encryption attribute needs to be removed.
func handleCreateFileNonAdsRemoveEncryption(path string, fileIn *os.File, isAlreadyExists bool) (file *os.File, err error) {
	// Encryption needs to be removed
	if isAlreadyExists {
		// First close the already existing file which was created.
		err = fileIn.Close()
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		// File already exists, so we need to remove it before adding the new file without encryption.
		err = os.Remove(path)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		return openFileWithCreate(path)
	} else {
		// File doesn't already exist, so we can simply create it without the encryption attribute.
		return openFileWithCreate(path)
	}
}

// handleCreateFileAds handles all the various combination of states while creating the ads related file if needed
func handleCreateFileAds(path string, mainPath string, fileIn *os.File, adsValues []string, hasAds, isAds, isEncryptionNeeded, isAlreadyEncrypted, isAlreadyExists bool) (file *os.File, err error) {
	// Deduce the secondary encryption handling flags
	isSameEncryption, isAddEncryption, isRemoveEncryption := deduceEncryptionHandlingFlags(isAlreadyExists, isEncryptionNeeded, isAlreadyEncrypted)

	if isSameEncryption {
		file, err = handleCreateFileAdsSameEncryption(path, fileIn, hasAds, isAds, isEncryptionNeeded, isAlreadyExists)
		if hasAds && isAlreadyExists {
			//check which streams need to be removed and remove the extra streams
			removeExtraStreams(path, adsValues, mainPath)
		}
		return file, err
	}

	if isAddEncryption {
		file, err = handleCreateFileAdsAddEncryption(path, mainPath, fileIn, isAds, isAlreadyExists)
		if hasAds && isAlreadyExists {
			//check which streams need to be removed and remove the extra streams
			removeExtraStreams(path, adsValues, mainPath)
		}
		return file, err
	}

	if isRemoveEncryption {
		file, err = handleCreateFileAdsRemoveEncryption(path, mainPath, fileIn, isAlreadyExists)
		if hasAds && isAlreadyExists {
			//check which streams need to be removed and remove the extra streams
			removeExtraStreams(path, adsValues, mainPath)
		}
		return file, err
	}
	return nil, errors.New("invalid case for create file ads")
}

// handleCreateFileNonAdsSameEncryption handles creation of non ads files where there is no change in encryption attribute
func handleCreateFileAdsSameEncryption(path string, fileIn *os.File, hasAds, isAds, isEncryptionNeeded, isAlreadyExists bool) (file *os.File, err error) {
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
			file, err = openFileWithoutCreate(path)
			if err != nil {
				if os.IsNotExist(err) {
					// We confirmed that the main file still doesn't exist after syncing.
					// Hence creating the file with the create flag.
					if isEncryptionNeeded {
						// First create the encrypted main file
						err = createEncryptedFile(path)
						if err != nil {
							return nil, err
						}
						// Then open the main file without create option
						return openFileWithoutCreate(path)
					} else {
						// Directly open the main file with create option as it should not be encrypted.
						return openFileWithCreate(path)
					}
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

// handleCreateFileNonAdsSameEncryption handles creation of non ads files where the encryption attribute needs to be added.
func handleCreateFileAdsAddEncryption(path string, mainPath string, fileIn *os.File, isAds, isAlreadyExists bool) (file *os.File, err error) {
	// Encryption needs to be added
	if isAlreadyExists {
		// First close the already existing file which was created.
		err = fileIn.Close()
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		// File already exists, so we need to remove it before adding the new file with encryption.
		// This could be a main file or an ads file. In either case the first time this block is hit,
		// the encrypted file would be recreated. Hence, we need to check again if the file was
		// recreated with the encryption flag before trying to remove it.
		file, err = openFileWithoutCreate(path)
		if err != nil {
			if os.IsNotExist(err) {
				// We confirmed that the main file doesn't exist after syncing.
				// Hence creating the file with the encryption flag.
				err = createEncryptedFile(path)
				if err != nil {
					return nil, err
				}
				return openFileWithoutCreate(path)
			} else {
				// Some other error occured so stop processing and return it.
				return nil, err
			}
		}
		// file exists
		var isAlreadyEncrypted bool
		isAlreadyEncrypted, err = isFileEncrypted(file)
		if err != nil {
			return nil, err
		}
		if isAlreadyEncrypted {
			// File is already encrypted. It may have been recreated with encryption flag by the other streams.
			return file, nil
		} else {
			// Close the file before creating a new encrypted file.
			err = file.Close()
			if err != nil && !os.IsNotExist(err) {
				return nil, err
			}

			// File is not yet encrypted. We need to re-create the main file with the encryption flag.
			err = removeAndCreateEncryptedFile(mainPath)
			if err != nil {
				return nil, err
			}
			// After creating the main file we need to open the file.
			if isAds {
				// If this is the Ads file, then after the main file was just created, we need to also
				// create the ads file stream with the create flag.
				return openFileWithCreate(path)
			} else {
				// If this is main file, then since it was just created, we just need to open the file
				// without create flag.
				return openFileWithoutCreate(path)
			}
		}
	} else {
		// File doesn't already exist, so we can simply create it with the encryption attribute.
		err = createEncryptedFile(path)
		if err != nil {
			return nil, err
		}
		return openFileWithoutCreate(path)
	}
}

// handleCreateFileNonAdsSameEncryption handles creation of non ads files where the encryption attribute needs to be removed.
func handleCreateFileAdsRemoveEncryption(path string, mainPath string, fileIn *os.File, isAlreadyExists bool) (file *os.File, err error) {
	// Encryption needs to be removed
	if isAlreadyExists {
		// First close the already existing file which was created.
		err = fileIn.Close()
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		// File already exists, so we need to remove it before adding the new file without encryption.
		// This could be a main file or an ads file. In either case the first time this block is hit,
		// the unencrypted file would be recreated. Hence, we need to check again if the file was
		// recreated without the encryption flag before trying to remove it.
		file, err = openFileWithoutCreate(path)
		if err != nil {
			if os.IsNotExist(err) {
				// We confirmed that the main file doesn't exist after syncing.
				// Hence creating the file with the create flag without encryption.
				return openFileWithCreate(path)
			} else {
				// Some other error occured so stop processing and return it.
				return nil, err
			}
		}

		var isAlreadyEncrypted bool
		isAlreadyEncrypted, err = isFileEncrypted(file)
		if err != nil {
			return nil, err
		}

		if !isAlreadyEncrypted {
			// File is already unencrypted. It may have been recreated without encryption flag by the other streams.
			return file, nil
		} else {
			// Close the file before creating a new unencrypted file.
			err = file.Close()
			if err != nil && !os.IsNotExist(err) {
				return nil, err
			}

			// File is not yet unencrypted. We need to re-create the main file without the encryption flag.
			err = os.Remove(mainPath)
			if err != nil && !os.IsNotExist(err) {
				return nil, err
			}
			// Now we create the file with create option, whether it is an ads or has ads.
			// The other calls will find that the file now already exists without encryption flag.
			return openFileWithCreate(path)
		}
	} else {
		// File doesn't already exist, so we can simply create it without the encryption attribute.
		return openFileWithCreate(path)
	}
}

// Helper methods

// removeExtraStreams removes any extra streams on the file which are not present in the
// backed up state in the generic attribute TypeHasAds.
func removeExtraStreams(path string, adsValues []string, mainPath string) {
	success, existingStreams, _ := fs.GetADStreamNames(path)
	if success {
		extraStreams := filterItems(adsValues, existingStreams)
		for _, extraStream := range extraStreams {
			streamToRemove := mainPath + extraStream
			err := os.Remove(streamToRemove)
			if err != nil {
				debug.Log("Error removing stream: %s : %s", streamToRemove, err)
			}
		}
	}
}

// filterItems filters out which items are in evalArray which are not in referenceArray
func filterItems(referenceArray, evalArray []string) (result []string) {
	// Create a map to store elements of referenceArray for fast lookup
	referenceArrayMap := make(map[string]bool)
	for _, item := range referenceArray {
		referenceArrayMap[item] = true
	}

	// Iterate through elements of evalArray
	for _, item := range evalArray {
		// Check if the item is not in referenceArray
		if !referenceArrayMap[item] {
			// Append to the result array
			result = append(result, item)
		}
	}

	return result
}

// openFileWithCreate opens the file with os.O_CREATE flag along with os.O_TRUNC and os.O_WRONLY
func openFileWithCreate(path string) (file *os.File, err error) {
	flags := os.O_CREATE | os.O_TRUNC | os.O_WRONLY
	return os.OpenFile(path, flags, 0600)
}

// openFileWithCreate opens the file without os.O_CREATE flag along with os.O_TRUNC and os.O_WRONLY
func openFileWithoutCreate(path string) (file *os.File, err error) {
	flags := os.O_TRUNC | os.O_WRONLY
	return os.OpenFile(path, flags, 0600)
}

// deduceEncryptionHandlingFlags deduces the flags for encryption handling based on the passed in state flags.
func deduceEncryptionHandlingFlags(isAlreadyExists bool, isEncryptionNeeded bool, isAlreadyEncrypted bool) (isSameEncryption bool, isAddEncryption bool, isRemoveEncryption bool) {
	if isAlreadyExists {
		if isEncryptionNeeded {
			// Based on the generic attributes of the file being restored, we expect the file to be encrypted.
			if isAlreadyEncrypted {
				// Simple case where file is already encrypted. No need to change encryption
				isSameEncryption = true
			} else {
				// File should be encrypted but not already encrypted. Need to add the encryption attrib to file.
				isAddEncryption = true
			}
		} else {
			// Based on the generic attributes of the file being restored, we expect the file to not be encrypted.
			if isAlreadyEncrypted {
				// File should be not encrypted by already encrypted. Need to remove the encryption attrib from file.
				isRemoveEncryption = true
			} else {
				// Simple case where file is already not encrypted. No need to change encryption
				isSameEncryption = true
			}
		}
	} else {
		isAddEncryption = isEncryptionNeeded
		isRemoveEncryption = !isEncryptionNeeded
		// isSameEncryption is false because file hasn't been created yet
	}
	return isSameEncryption, isAddEncryption, isRemoveEncryption
}

// isFileEncrypted checks if file is encrypted
func isFileEncrypted(file *os.File) (isFileEncrypted bool, err error) {
	var fi os.FileInfo
	fi, err = file.Stat()
	if err == nil {
		stat, ok := (fi.Sys()).(*syscall.Win32FileAttributeData)
		if ok && stat != nil {
			fileAttributes := stat.FileAttributes

			isFileEncrypted = fileAttributes&windows.FILE_ATTRIBUTE_ENCRYPTED != 0
		}
	}
	return isFileEncrypted, err
}

// createEncryptedFile creates a file with windows.FILE_ATTRIBUTE_ENCRYPTED file attribute.
func createEncryptedFile(path string) (err error) {
	var ptr *uint16
	ptr, err = windows.UTF16PtrFromString(path)
	if err == nil {
		var handle windows.Handle
		handle, err = windows.CreateFile(ptr, uint32(windows.GENERIC_READ|windows.GENERIC_WRITE), uint32(windows.FILE_SHARE_READ), nil, uint32(windows.CREATE_ALWAYS), uint32(windows.FILE_ATTRIBUTE_ENCRYPTED|windows.FILE_ATTRIBUTE_COMPRESSED), 0)
		if err == nil {
			err = windows.CloseHandle(handle)
		}
	}
	return err
}

// removeAndCreateEncryptedFile removes the file before creating the file with encrypted attribute
func removeAndCreateEncryptedFile(path string) (err error) {
	err = os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	err = createEncryptedFile(path)
	return err
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

// removeMutex removes the mutex for the specified path
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

// checkGenericAttributes checks for the required generic attributes
func checkGenericAttributes(fileInfo *fileInfo, path string) (adsValues []string, hasAds, isAds, isEncryptionNeeded bool) {
	attrs := fileInfo.attrs
	if len(attrs) > 0 {
		adsBytes := restic.GetGenericAttribute(restic.TypeHasADS, attrs)
		adsString := string(adsBytes)
		adsValues = strings.Split(adsString, restic.AdsSeparator)

		hasAds = adsBytes != nil
		isAds = restic.GetGenericAttribute(restic.TypeIsADS, attrs) != nil
		fileAttrBytes := restic.GetGenericAttribute(restic.TypeFileAttribute, attrs)
		if len(fileAttrBytes) > 0 {
			fileAttributes := binary.LittleEndian.Uint32(fileAttrBytes)
			isEncryptionNeeded = fileAttributes&windows.FILE_ATTRIBUTE_ENCRYPTED != 0
		}
	}
	return adsValues, hasAds, isAds, isEncryptionNeeded
}

// clearReadonly removes the readonly flag for the main file
// as it will be set again while applying metadata in the next pass if required.
func clearReadonly(isAds bool, path string, file *os.File) error {
	if isAds {
		// If this is an ads stream we need to get the main file for setting attributes.
		path = fs.TrimAds(path)
	}
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	fileAttributes, err := windows.GetFileAttributes(ptr)
	if err != nil {
		return err
	}
	if isReadonly(fileAttributes) {
		// Clear FILE_ATTRIBUTE_READONLY flag
		fileAttributes &= ^uint32(windows.FILE_ATTRIBUTE_READONLY)
		err = windows.SetFileAttributes(ptr, fileAttributes)
		if err != nil {
			return err
		}
	}
	return nil
}

// isAccessDenied checks if the error is ERROR_ACCESS_DENIED or a Path error due to windows.ERROR_ACCESS_DENIED
func isAccessDenied(err error) bool {
	isAccessDenied := isAccessDeniedError(err)
	if !isAccessDenied {
		if e, ok := err.(*os.PathError); ok {
			isAccessDenied = isAccessDeniedError(e.Err)
		}
	}
	return isAccessDenied
}

// isAccessDeniedError checks if the error is ERROR_ACCESS_DENIED
func isAccessDeniedError(err error) bool {
	return fs.IsErrorOfType(err, windows.ERROR_ACCESS_DENIED)
}

// isReadonly checks if the fileAtributes have readonly bit
func isReadonly(fileAttributes uint32) bool {
	return fileAttributes&windows.FILE_ATTRIBUTE_READONLY != 0
}
