// Kernel events (--kernel-events) are Linux only, and so are the helpers
// that keep the sockets of a dial: sameSocket relies on sockfs giving every
// socket an inode of its own, which other systems (macOS: 0) do not.

package client

import (
	"net"
	"syscall"
)

// dupSocket duplicates a socket descriptor, close-on-exec.
func dupSocket(fd uintptr) (int, error) {
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	nfd, err := syscall.Dup(int(fd))
	if err != nil {
		return -1, err
	}
	syscall.CloseOnExec(nfd)
	return nfd, nil
}

// sameSocket reports whether fd is a descriptor of the socket of conn.
func sameSocket(fd int, conn net.Conn) bool {
	sc, ok := conn.(syscall.Conn)
	if !ok {
		return false
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return false
	}
	var a, b syscall.Stat_t
	if err := syscall.Fstat(fd, &a); err != nil {
		return false
	}
	var statErr error
	if err := raw.Control(func(cfd uintptr) { statErr = syscall.Fstat(int(cfd), &b) }); err != nil || statErr != nil {
		return false
	}
	return a.Dev == b.Dev && a.Ino == b.Ino
}

// closeFD closes a descriptor that dupSocket returned.
func closeFD(fd int) { _ = syscall.Close(fd) }
