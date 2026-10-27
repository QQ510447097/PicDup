//go:build !windows

package core

import "runtime"

// recycleSupported reports whether the OS Recycle Bin can be used.
func recycleSupported() bool { return false }

func recycleFiles(paths []string) (moved int, bytes int64, failures []TrashFailure) {
	for _, p := range paths {
		failures = append(failures, TrashFailure{Path: p, Error: "当前平台不支持回收站"})
	}
	_ = runtime.GOOS
	return 0, 0, failures
}
