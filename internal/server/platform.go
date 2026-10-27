package server

import (
	"os/exec"
	"runtime"
)

// openPath opens a file with the OS default application.
func openPath(path string) error {
	if runtime.GOOS == "windows" {
		return exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", path).Start()
	}
	if runtime.GOOS == "darwin" {
		return exec.Command("open", path).Start()
	}
	return exec.Command("xdg-open", path).Start()
}

// revealPath opens the file manager with the file selected.
func revealPath(path string) error {
	if runtime.GOOS == "windows" {
		return exec.Command("explorer.exe", "/select,"+path).Start()
	}
	if runtime.GOOS == "darwin" {
		return exec.Command("open", "-R", path).Start()
	}
	return exec.Command("xdg-open", path).Start()
}
