//go:build !windows

package api

import "syscall"

func getDiskSpace(path string) (freeBytes, totalDiskBytes uint64) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err == nil {
		freeBytes = stat.Bavail * uint64(stat.Bsize)
		totalDiskBytes = stat.Blocks * uint64(stat.Bsize)
	}
	return
}
