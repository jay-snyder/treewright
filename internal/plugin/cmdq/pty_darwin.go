package cmdq

import (
	"bytes"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// ptyName grants and unlocks the pseudo-terminal behind a /dev/ptmx
// descriptor and names its other end: grantpt, unlockpt and ptsname, which on
// macOS are three ioctls. The third writes the name into a buffer the caller
// supplies — 128 bytes, the size the kernel's own declaration of it gives.
//
// That one goes through the standard library's syscall package rather than
// x/sys, which on macOS deprecates raw system call numbers in favor of its
// libSystem wrappers — and none of those takes a buffer to fill. The standard
// library's Syscall reaches the same ioctl through libSystem's own entry point,
// and is how the pty packages people reach for on macOS make this call.
func ptyName(fd int) (string, error) {
	if err := unix.IoctlSetInt(fd, unix.TIOCPTYGRANT, 0); err != nil {
		return "", err
	}
	if err := unix.IoctlSetInt(fd, unix.TIOCPTYUNLK, 0); err != nil {
		return "", err
	}
	var name [128]byte
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(unix.TIOCPTYGNAME), uintptr(unsafe.Pointer(&name[0]))); errno != 0 {
		return "", errno
	}
	end := bytes.IndexByte(name[:], 0)
	if end < 0 {
		end = len(name)
	}
	return string(name[:end]), nil
}
