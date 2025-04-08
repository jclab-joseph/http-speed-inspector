package tcpawarehttp

import (
	"context"
	"net"
)

type DialCtx struct {
	LocalAddr  net.Addr
	RemoteAddr net.Addr
}

var defaultDialer net.Dialer

func DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	conn, err := defaultDialer.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	di, ok := ctx.Value("dialInfo").(*DialCtx)
	if ok {
		di.LocalAddr = conn.LocalAddr()
		di.RemoteAddr = conn.RemoteAddr()
	}
	return conn, nil
}

func WithDialCtx(ctx context.Context) (context.Context, *DialCtx) {
	di := &DialCtx{}
	return context.WithValue(ctx, "dialInfo", di), di
}
