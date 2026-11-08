//go:build linux

package server

import "syscall"

func init() {
	// The host is not on the network: another network error accept(2) says
	// to retry, which only Linux defines.
	transientAcceptErrors = append(transientAcceptErrors, syscall.ENONET)
}
