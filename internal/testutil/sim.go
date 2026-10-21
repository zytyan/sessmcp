// Package testutil contains real TCP/SSH device simulators used by E2E tests.
package testutil

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ziutek/telnet"
	"golang.org/x/crypto/ssh"
)

func Listen(t *testing.T, handler func(net.Conn)) string {
	t.Helper()
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	var mu sync.Mutex
	var conns []net.Conn
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
			wg.Add(1)
			go func() { defer wg.Done(); defer c.Close(); handler(c) }()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		for _, c := range conns {
			_ = c.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	return ln.Addr().String()
}

// Switch emulates mode transitions, ANSI output, a pager, fragmented prompts,
// and a low-bandwidth serial terminal. It intentionally has no shell exit code.
func Switch(rw io.ReadWriter) {
	r := bufio.NewReader(rw)
	mode := "switch> "
	write := func(s string) { _, _ = io.WriteString(rw, s) }
	write("\x1b[2JWelcome to serial switch\r\nswitch> ")
	for {
		line, e := r.ReadString('\n')
		if e != nil {
			return
		}
		cmd := strings.TrimSpace(line)

		switch cmd {
		case "enable":
			mode = "switch# "
		case "configure terminal":
			mode = "switch(config)# "
		case "interface ethernet 1":
			mode = "switch(config-if)# "
		case "end":
			mode = "switch# "
		case "show version":
			write("\r\nSwitchOS 1.0\r\n")
		case "show running-config":
			write("\r\nhostname switch\r\n--More--")
			b, e := r.ReadByte()
			if e != nil {
				return
			}
			if b == ' ' {
				write("\r\ninterface ethernet 1\r\n description serial\r\n")
			}
		case "slow":
			for _, b := range []byte("\r\nslow serial output\r\n") {
				_, _ = rw.Write([]byte{b})
				time.Sleep(4 * time.Millisecond)
			}
		case "hang":
			continue
		case "quit":
			return
		default:
			write("\r\n% unknown command\r\n")
		}
		for _, b := range []byte(mode) {
			_, _ = rw.Write([]byte{b})
			time.Sleep(time.Millisecond)
		}
	}
}
func TelnetSwitch(t *testing.T) string {
	return Listen(t, func(c net.Conn) { // Split IAC negotiation across writes and read the library's response.
		for _, b := range []byte{255, 251, 1} {
			_, _ = c.Write([]byte{b})
			time.Sleep(time.Millisecond)
		}
		tc, e := telnet.NewConn(c)
		if e != nil {
			return
		}
		Switch(tc)
	})
}
func SSH(t *testing.T) (string, string) {
	t.Helper()
	_, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	signer, e := ssh.NewSignerFromKey(key)
	if e != nil {
		t.Fatal(e)
	}
	config := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) { return nil, nil }}
	config.AddHostKey(signer)
	addr := Listen(t, func(raw net.Conn) {
		_ = raw.SetDeadline(time.Now().Add(10 * time.Second))
		sc, channels, requests, e := ssh.NewServerConn(raw, config)
		if e != nil {
			return
		}
		_ = raw.SetDeadline(time.Time{})
		defer sc.Close()
		go ssh.DiscardRequests(requests)
		var wg sync.WaitGroup
		defer wg.Wait()
		for nc := range channels {
			if nc.ChannelType() != "session" {
				_ = nc.Reject(ssh.UnknownChannelType, "session only")
				continue
			}
			ch, reqs, e := nc.Accept()
			if e != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer ch.Close()
				for req := range reqs {
					switch req.Type {
					case "pty-req":
						_ = req.Reply(true, nil)
					case "shell":
						_ = req.Reply(true, nil)
						Switch(ch)
						return
					case "exec":
						var p struct{ Command string }
						if ssh.Unmarshal(req.Payload, &p) != nil {
							return
						}
						_ = req.Reply(true, nil)
						code := uint32(0)
						switch p.Command {
						case "both":
							_, _ = io.WriteString(ch, "stdout\n")
							_, _ = io.WriteString(ch.Stderr(), "stderr\n")
						case "exit7":
							code = 7
							_, _ = io.WriteString(ch.Stderr(), "failed\n")
						case "slow":
							_, _ = io.WriteString(ch, "first\n")
							time.Sleep(150 * time.Millisecond)
							_, _ = io.WriteString(ch.Stderr(), "second\n")
						case "hang":
							_, _ = io.Copy(io.Discard, ch)
							return
						default:
							_, _ = io.WriteString(ch, p.Command+"\n")
						}
						b := make([]byte, 4)
						binary.BigEndian.PutUint32(b, code)
						_, _ = ch.SendRequest("exit-status", false, b)
						return
					default:
						_ = req.Reply(false, nil)
					}
				}
			}()
		}
	})
	return addr, ssh.FingerprintSHA256(signer.PublicKey())
}
