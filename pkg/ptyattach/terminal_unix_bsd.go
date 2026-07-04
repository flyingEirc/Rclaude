//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package ptyattach

import "golang.org/x/sys/unix"

const (
	ioctlReadTermios  = unix.TIOCGETA
	ioctlWriteTermios = unix.TIOCSETA
)
