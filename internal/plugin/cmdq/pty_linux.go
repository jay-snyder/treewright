package cmdq

import (
	"strconv"

	"golang.org/x/sys/unix"
)

// ptyName unlocks the pseudo-terminal behind a /dev/ptmx descriptor and names
// its other end: unlockpt and ptsname, which on Linux are an ioctl each. There
// is no grantpt to do — devpts gives the terminal its owner and mode itself.
func ptyName(fd int) (string, error) {
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		return "", err
	}
	n, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		return "", err
	}
	return "/dev/pts/" + strconv.Itoa(n), nil
}
