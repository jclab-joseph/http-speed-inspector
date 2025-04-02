//go:build !windows
// +build !windows

package tcpinfo

import (
	"errors"
	"golang.org/x/sys/unix"
	"log"
	"net"
)

type UnixTCPInfo struct {
	unix.TCPInfo
}

func (t *UnixTCPInfo) GetTotalRetrans() int {
	return int(t.Total_retrans)
}

func (t *UnixTCPInfo) GetSegsOut() int {
	return int(t.Segs_out)
}

func GetTcpInfo(conn net.Conn) (*UnixTCPInfo, error) {
	// Get TCP info before closing
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		file, err := tcpConn.File()
		if err != nil {
			log.Printf("Error getting file descriptor: %v", err)
			return nil, err
		}
		defer file.Close()

		info, err := unix.GetsockoptTCPInfo(int(file.Fd()), unix.IPPROTO_TCP, unix.TCP_INFO)
		if err != nil {
			log.Printf("Error getting TCP info: %v", err)
			return nil, err
		} else {
			return &UnixTCPInfo{
				*info,
			}, nil
		}
	}
	return nil, errors.New("no tcp conn")
}
