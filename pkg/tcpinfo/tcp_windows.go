package tcpinfo

import (
	"errors"
	"fmt"
	"net"
	"syscall"
	"unsafe"
)

type TCPInfoV0 struct {
	State             uint32
	Mss               uint32
	ConnectionTimeMs  uint64
	TimestampsEnabled bool
	RttUs             uint32
	MinRttUs          uint32
	BytesInFlight     uint32
	Cwnd              uint32
	SndWnd            uint32
	RcvWnd            uint32
	RcvBuf            uint32
	BytesOut          uint64
	BytesIn           uint64
	BytesReordered    uint32
	BytesRetrans      uint32
	FastRetrans       uint32
	DupAcksIn         uint32
	TimeoutEpisodes   uint32
	SynRetrans        uint8
}

const (
	SIO_TCP_INFO = syscall.IOC_INOUT | syscall.IOC_VENDOR | 39
)

func GetTcpInfo(conn net.Conn) (*TCPInfoV0, error) {
	// Get TCP info before closing
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		rawConn, err := tcpConn.SyscallConn()
		if err != nil {
			return nil, fmt.Errorf("failed to get syscall conn: %w", err)
		}

		var info TCPInfoV0
		var bytesReturned uint32
		var infoErr error

		inbuf := uint32(0)

		err = rawConn.Control(func(fd uintptr) {
			// Windows에서 WSAIoctl을 사용하여 TCP_INFO_v0 정보를 가져옴
			infoErr = syscall.WSAIoctl(
				syscall.Handle(fd),
				SIO_TCP_INFO,
				(*byte)(unsafe.Pointer(&inbuf)),
				uint32(unsafe.Sizeof(inbuf)),
				(*byte)(unsafe.Pointer(&info)),
				uint32(unsafe.Sizeof(info)),
				&bytesReturned,
				nil,
				0,
			)
		})

		if err != nil {
			return nil, fmt.Errorf("control function error: %w", err)
		}

		if infoErr != nil {
			var errno syscall.Errno
			errors.As(infoErr, &errno)
			return nil, fmt.Errorf("WSAIoctl error: %d, %w", errno, infoErr)
		}

		return &info, nil
	}

	return nil, errors.New("no tcp conn")
}
