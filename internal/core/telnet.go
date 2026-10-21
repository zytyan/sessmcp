package core

import (
	"context"
	"io"
	"net"

	"github.com/zytyan/sessmcp/internal/telnet"
)

func (s *Session) connectTelnet(ctx context.Context, o Options) error {
	raw, e := (&net.Dialer{Timeout: IOLimit}).DialContext(ctx, "tcp", o.Address)
	if e != nil {
		return e
	}
	s.closer = raw
	c := telnet.NewConn(raw, IOLimit)
	s.input = c
	go func() {
		_, e := io.Copy(s, c)
		if e == nil {
			e = io.EOF
		}
		s.disconnect("Telnet disconnected: " + e.Error())
	}()
	return nil
}
