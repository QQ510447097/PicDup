//go:build !windows

package core

import "strings"

func pathIsHidden(_ string, name string) bool {
	return strings.HasPrefix(name, ".")
}
