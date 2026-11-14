package main

import (
	"bytes"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

const getTermios = unix.TIOCGETA

func ptsName(master *os.File) (string, error) {
	fd := int(master.Fd())
	if err := unix.IoctlSetInt(fd, unix.TIOCPTYGRANT, 0); err != nil {
		return "", err
	}
	if err := unix.IoctlSetInt(fd, unix.TIOCPTYUNLK, 0); err != nil {
		return "", err
	}
	var name [128]byte
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.TIOCPTYGNAME, uintptr(unsafe.Pointer(&name[0]))); errno != 0 {
		return "", errno
	}
	return string(name[:bytes.IndexByte(name[:], 0)]), nil
}
