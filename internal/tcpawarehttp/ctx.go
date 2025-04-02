package tcpawarehttp

import (
	"context"
	"github.com/hashicorp/golang-lru/v2/expirable"
	"net"
	"reflect"
	"sync"
	"time"
	"unsafe"
)

var mutex sync.Mutex
var connCache *expirable.LRU[unsafe.Pointer, *ConnCtx]

func init() {
	connCache = expirable.NewLRU[unsafe.Pointer, *ConnCtx](10000, nil, time.Hour)
}

type TcpCtx struct {
	NativeConn net.Conn
}

type ConnCtx struct {
	PrevSegsOut      int
	PrevTotalRetrans int
}

func GetTcpCtx(ctx context.Context) *TcpCtx {
	v, ok := ctx.Value("tcpCtx").(*TcpCtx)
	if ok {
		return v
	}
	return nil
}

func WithTcpCtx(ctx context.Context, conn net.Conn) (context.Context, *TcpCtx) {
	v := &TcpCtx{
		NativeConn: conn,
	}
	return context.WithValue(ctx, "tcpCtx", v), v
}

func (c *TcpCtx) GetConnCtx() *ConnCtx {
	mutex.Lock()
	defer mutex.Unlock()
	connPtr := reflect.ValueOf(c.NativeConn).UnsafePointer()
	connCtx, has := connCache.Get(connPtr)
	if !has || connCtx == nil {
		connCtx = &ConnCtx{}
		connCache.Add(connPtr, connCtx)
	}
	return connCtx
}
