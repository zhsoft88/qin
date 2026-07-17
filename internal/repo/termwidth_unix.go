// +build !windows

package repo

import (
	"syscall"
	"unsafe"
)

// termWidthFromSystem attempts to get terminal width via ioctl TIOCGWINSZ on Unix.
func termWidthFromSystem() int {
	fd, err := syscall.Open("/dev/tty", syscall.O_RDONLY, 0)
	if err != nil {
		return 0
	}
	defer syscall.Close(fd)

	var ws struct {
		Row    uint16
		Col    uint16
		Xpixel uint16
		Ypixel uint16
	}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(tiocgwinsz()), uintptr(unsafe.Pointer(&ws)))
	if errno != 0 {
		return 0
	}
	if ws.Col > 0 {
		return int(ws.Col)
	}
	return 0
}

func tiocgwinsz() uintptr {
	// TIOCGWINSZ = 0x5413 on Linux, 0x40087468 on macOS/FreeBSD
	return 0x5413
}
