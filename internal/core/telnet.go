package core

import (
	"context"
	"io"
	"net"
	"sync"
	"time"

	"github.com/ziutek/telnet"
)

// deadlineConn adds bounded writes to both application data and negotiation
// replies produced by the established Telnet library. It contains no Telnet
// framing or option negotiation implementation.
type deadlineConn struct {
	net.Conn
	mu sync.Mutex
}

func (c *deadlineConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.SetWriteDeadline(time.Now().Add(IOLimit))
	defer c.SetWriteDeadline(time.Time{})
	total := 0
	for total < len(p) {
		n, e := c.Conn.Write(p[total:])
		total += n
		if e != nil {
			return total, e
		}
		if n == 0 {
			return total, io.ErrShortWrite
		}
	}
	return total, nil
}
func (s *Session) connectTelnet(ctx context.Context, o Options) error {
	raw, e := (&net.Dialer{Timeout: IOLimit}).DialContext(ctx, "tcp", o.Address)
	if e != nil {
		return e
	}
	s.closer = raw
	c, e := telnet.NewConn(&deadlineConn{Conn: raw})
	if e != nil {
		return e
	}
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
