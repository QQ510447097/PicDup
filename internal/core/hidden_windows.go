//go:build windows

package core

import (
	"strings"
	"syscall"
)

// pathIsHidden reports dot-prefixed names and the Windows hidden attribute.
func pathIsHidden(path, name string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	attrs, err := syscall.GetFileAttributes(p)
	if err != nil {
		return false
	}
	return attrs&syscall.FILE_ATTRIBUTE_HIDDEN != 0
}
