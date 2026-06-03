//go:build !windows
// +build !windows

package api

import "syscall"

func getDiskInfo(path string) (*diskInfo, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return nil, err
	}
	// Guard against uint64 overflow when converting to int64.
	clamp := func(v uint64) int64 {
		if v > 1<<63-1 {
			return 1<<63 - 1
		}
		return int64(v)
	}
	//nolint:unconvert // Bsize is int64 on 64-bit but int32 on 32-bit (arm)
	bsize := int64(stat.Bsize)
	return &diskInfo{
		available: clamp(stat.Bavail) * bsize,
		total:     clamp(stat.Blocks) * bsize,
		used:      (clamp(stat.Blocks) - clamp(stat.Bfree)) * bsize,
	}, nil
}
