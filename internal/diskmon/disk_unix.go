//go:build linux || darwin || freebsd || openbsd || netbsd

package diskmon

import "syscall"

func getDiskUsage(path string) (available, total int64, err error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0, err
	}
	// Available space in bytes (guard against uint64 overflow for int64)
	clamp := func(v uint64) int64 {
		if v > 1<<63-1 {
			return 1<<63 - 1
		}
		return int64(v)
	}
	//nolint:unconvert // Bsize is int64 on 64-bit but int32 on 32-bit (arm)
	bsize := int64(stat.Bsize)
	available = clamp(stat.Bavail) * bsize
	total = clamp(stat.Blocks) * bsize
	return available, total, nil
}
