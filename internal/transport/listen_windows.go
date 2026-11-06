package transport

import (
	"net"
	"syscall"
)

func ListenConfig() net.ListenConfig {
	return net.ListenConfig{Control: func(network, address string, raw syscall.RawConn) error {
		var err error
		if e := raw.Control(func(fd uintptr) { err = syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, -5, 1) }); e != nil {
			return e
		}
		return err
	}}
}
