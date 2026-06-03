//go:build windows
// +build windows

package api

import (
	"fmt"
	"golang.org/x/sys/windows"
	"path/filepath"
)

func getDiskInfo(path string) (*diskInfo, error) {
	// Get the volume path (drive letter on Windows)
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}

	volumePath := filepath.VolumeName(absPath)
	if volumePath == "" {
		return nil, fmt.Errorf("unable to determine volume for path: %s", path)
	}
	volumePath += "\\"

	// Convert to UTF-16 pointer
	volumePathPtr, err := windows.UTF16PtrFromString(volumePath)
	if err != nil {
		return nil, err
	}

	var freeBytes, totalBytes, availBytes uint64
	err = windows.GetDiskFreeSpaceEx(volumePathPtr, &availBytes, &totalBytes, &freeBytes)
	if err != nil {
		return nil, err
	}

	return &diskInfo{
		available: int64(availBytes),
		total:     int64(totalBytes),
		used:      int64(totalBytes - freeBytes),
	}, nil
}
