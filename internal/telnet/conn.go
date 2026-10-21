// Package telnet provides a bounded Telnet stream for device consoles.
// A connection supports one reader and concurrent writers. Application input is
// sent exactly as supplied except for the IAC escaping required by RFC 854.
package telnet

import (
	"bufio"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

const (
	iac       = 255
	se        = 240
	sb        = 250
	will      = 251
	wont      = 252
	do        = 253
	dont      = 254
	maxSubneg = 8192
)

// Conn strips Telnet control sequences from output. Negotiation state belongs
// to the single reader; writes (including negotiation replies) are serialized.
type Conn struct {
	net.Conn
	reader                                     *bufio.Reader
	writeMu                                    sync.Mutex
	writeTimeout                               time.Duration
	state, verb, option                        byte
	sub                                        []byte
	remote, local, refusedRemote, refusedLocal [256]bool
}

func NewConn(c net.Conn, writeTimeout time.Duration) *Conn {
	if writeTimeout <= 0 {
		writeTimeout = 10 * time.Second
	}
	return &Conn{Conn: c, reader: bufio.NewReaderSize(c, 4096), writeTimeout: writeTimeout}
}
func (c *Conn) writeRaw(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if e := c.Conn.SetWriteDeadline(time.Now().Add(c.writeTimeout)); e != nil {
		return 0, e
	}
	defer c.Conn.SetWriteDeadline(time.Time{})
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
func (c *Conn) Write(p []byte) (int, error) {
	// Keep memory bounded even for callers other than sessmcp's input limiter.
	consumed := 0
	for len(p) > 0 {
		size := len(p)
		if size > 4096 {
			size = 4096
		}
		chunk := p[:size]
		encoded := make([]byte, 0, size*2)
		for _, b := range chunk {
			encoded = append(encoded, b)
			if b == iac {
				encoded = append(encoded, b)
			}
		}
		n, e := c.writeRaw(encoded)
		if e != nil { // Report original bytes represented by completely written encodings.
			for _, b := range chunk {
				width := 1
				if b == iac {
					width = 2
				}
				if n < width {
					break
				}
				n -= width
				consumed++
			}
			return consumed, e
		}
		consumed += size
		p = p[size:]
	}
	return consumed, nil
}
func (c *Conn) reply(command, option byte) error {
	_, e := c.writeRaw([]byte{iac, command, option})
	return e
}
func (c *Conn) negotiate(command, option byte) error {
	switch command {
	case will:
		allowed := option == 0 || option == 1 || option == 3 // binary, server echo, suppress-go-ahead
		if allowed {
			if !c.remote[option] {
				c.remote[option] = true
				return c.reply(do, option)
			}
		} else if !c.refusedRemote[option] {
			c.refusedRemote[option] = true
			return c.reply(dont, option)
		}
	case wont:
		c.refusedRemote[option] = false
		if c.remote[option] {
			c.remote[option] = false
			return c.reply(dont, option)
		}
	case do:
		allowed := option == 0 || option == 3 || option == 24 || option == 31 // binary, SGA, terminal type, window size
		if allowed {
			if !c.local[option] {
				c.local[option] = true
				if e := c.reply(will, option); e != nil {
					return e
				}
				if option == 31 {
					return c.subreply(31, []byte{0, 80, 0, 24})
				}
			}
		} else if !c.refusedLocal[option] {
			c.refusedLocal[option] = true
			return c.reply(wont, option)
		}
	case dont:
		c.refusedLocal[option] = false
		if c.local[option] {
			c.local[option] = false
			return c.reply(wont, option)
		}
	}
	return nil
}
func (c *Conn) subreply(option byte, p []byte) error {
	b := []byte{iac, sb, option}
	for _, v := range p {
		b = append(b, v)
		if v == iac {
			b = append(b, v)
		}
	}
	b = append(b, iac, se)
	_, e := c.writeRaw(b)
	return e
}
func (c *Conn) finishSub() error {
	if c.option == 24 && c.local[24] && len(c.sub) == 1 && c.sub[0] == 1 {
		return c.subreply(24, []byte{0, 'v', 't', '1', '0', '0'})
	}
	return nil
}
func (c *Conn) addSub(b byte) error {
	if len(c.sub) >= maxSubneg {
		return errors.New("Telnet subnegotiation exceeds 8192 bytes")
	}
	c.sub = append(c.sub, b)
	return nil
}
func (c *Conn) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		// Do not hold back decoded data while a split control sequence is incomplete.
		if n > 0 && c.reader.Buffered() == 0 {
			return n, nil
		}
		b, e := c.reader.ReadByte()
		if e != nil {
			if e == io.EOF && c.state != 0 {
				e = io.ErrUnexpectedEOF
			}
			return n, e
		}
		switch c.state {
		case 0:
			if b == iac {
				c.state = 1
			} else {
				p[n] = b
				n++
			}
		case 1:
			switch b {
			case iac:
				p[n] = iac
				n++
				c.state = 0
			case will, wont, do, dont:
				c.verb = b
				c.state = 2
			case sb:
				c.state = 3
			default:
				c.state = 0
			} // NOP, GA and other single-byte controls have no output.
		case 2:
			c.state = 0
			if e = c.negotiate(c.verb, b); e != nil {
				return n, e
			}
		case 3:
			c.option = b
			c.sub = c.sub[:0]
			c.state = 4
		case 4:
			if b == iac {
				c.state = 5
			} else if e = c.addSub(b); e != nil {
				return n, e
			}
		case 5:
			switch b {
			case se:
				c.state = 0
				if e = c.finishSub(); e != nil {
					return n, e
				}
			case iac:
				c.state = 4
				if e = c.addSub(iac); e != nil {
					return n, e
				}
			default:
				return n, errors.New("invalid Telnet subnegotiation escape")
			}
		}
	}
	return n, nil
}
