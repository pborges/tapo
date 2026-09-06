//go:build !aix && !android && !darwin && !dragonfly && !freebsd && !hurd && !illumos && !ios && !linux && !netbsd && !openbsd && !solaris

package tapo

import "net"

func enableBroadcast(*net.UDPConn) error { return nil }
