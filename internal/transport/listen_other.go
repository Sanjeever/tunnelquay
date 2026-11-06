//go:build !windows

package transport

import "net"

func ListenConfig() net.ListenConfig { return net.ListenConfig{} }
