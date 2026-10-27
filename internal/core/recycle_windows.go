//go:build windows

package core

import (
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// recycleSupported reports whether the OS Recycle Bin can be used.
func recycleSupported() bool { return true }

type shFileOpStruct struct {
	hwnd                  uintptr
	wFunc                 uint32
	pFrom                 *uint16
	pTo                   *uint16
	fFlags                uint16
	fAnyOperationsAborted int32
	hNameMappings         uintptr
	lpszProgressTitle     *uint16
}

var (
	shell32                     = windows.NewLazySystemDLL("shell32.dll")
	procSHFileOperationW        = shell32.NewProc("SHFileOperationW")
	foDelete             uint32 = 3
	// FOF_ALLOWUNDO | FOF_NOCONFIRMATION | FOF_SILENT | FOF_NOERRORUI | FOF_NOCONFIRMMKDIR
	foFlags uint16 = 0x0040 | 0x0010 | 0x0004 | 0x0400 | 0x0200
)

func recycleFiles(paths []string) (moved int, bytes int64, failures []TrashFailure) {
	if len(paths) == 0 {
		return 0, 0, nil
	}
	sizes := make(map[string]int64, len(paths))
	var valid []string
	var buf []uint16
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil || st.IsDir() {
			failures = append(failures, TrashFailure{Path: p, Error: "文件不存在或不是普通文件"})
			continue
		}
		u, err := windows.UTF16FromString(p)
		if err != nil {
			failures = append(failures, TrashFailure{Path: p, Error: "路径无效"})
			continue
		}
		sizes[p] = st.Size()
		valid = append(valid, p)
		buf = append(buf, u...)
	}
	if len(valid) == 0 {
		return 0, 0, failures
	}
	buf = append(buf, 0)
	op := shFileOpStruct{
		wFunc:  foDelete,
		pFrom:  &buf[0],
		fFlags: foFlags,
	}
	ret, _, _ := procSHFileOperationW.Call(uintptr(unsafe.Pointer(&op)))
	runtime.KeepAlive(buf)
	for _, p := range valid {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			moved++
			bytes += sizes[p]
			continue
		}
		msg := "未能移入回收站"
		if ret != 0 {
			msg = fmt.Sprintf("回收站操作失败 (code %d)", ret)
		} else if op.fAnyOperationsAborted != 0 {
			msg = "操作已取消"
		}
		failures = append(failures, TrashFailure{Path: p, Error: msg})
	}
	if len(failures) == 0 {
		failures = nil
	}
	return moved, bytes, failures
}
