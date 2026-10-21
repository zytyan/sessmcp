package telnet

import (
	"bytes"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func wire(t *testing.T, script func(net.Conn) error, want []byte) {
	t.Helper()
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	_ = server.SetDeadline(time.Now().Add(3 * time.Second))
	done := make(chan error, 1)
	go func() { defer server.Close(); done <- script(server) }()
	got, e := io.ReadAll(NewConn(client, time.Second))
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("output %q, want %q", got, want)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
}
func send(c net.Conn, b []byte) error { _, e := c.Write(b); return e }
func expect(c net.Conn, want []byte) error {
	b := make([]byte, len(want))
	if _, e := io.ReadFull(c, b); e != nil {
		return e
	}
	if !bytes.Equal(b, want) {
		return errors.New("unexpected wire response: " + string(b))
	}
	return nil
}
func TestFragmentedNegotiationAndEscapedIAC(t *testing.T) {
	wire(t, func(c net.Conn) error {
		for _, b := range []byte{iac, will, 1} {
			if e := send(c, []byte{b}); e != nil {
				return e
			}
		}
		if e := expect(c, []byte{iac, do, 1}); e != nil {
			return e
		}
		return send(c, []byte{'a', iac, iac, 'b', iac, 241, 'c', iac, 249, 'd'})
	}, []byte{'a', iac, 'b', 'c', 'd'})
}
func TestUnknownOptionsAndDuplicateSuppression(t *testing.T) {
	wire(t, func(c net.Conn) error {
		if e := send(c, []byte{iac, will, 42, iac, will, 42, iac, do, 43, iac, do, 43}); e != nil {
			return e
		}
		if e := expect(c, []byte{iac, dont, 42, iac, wont, 43}); e != nil {
			return e
		}
		return send(c, []byte("ok"))
	}, []byte("ok"))
}
func TestTerminalTypeWindowSizeAndSubnegotiation(t *testing.T) {
	wire(t, func(c net.Conn) error {
		if e := send(c, []byte{iac, do, 24}); e != nil {
			return e
		}
		if e := expect(c, []byte{iac, will, 24}); e != nil {
			return e
		}
		for _, b := range []byte{iac, sb, 24, 1, iac, se} {
			if e := send(c, []byte{b}); e != nil {
				return e
			}
		}
		if e := expect(c, []byte{iac, sb, 24, 0, 'v', 't', '1', '0', '0', iac, se}); e != nil {
			return e
		}
		if e := send(c, []byte{iac, do, 31}); e != nil {
			return e
		}
		if e := expect(c, []byte{iac, will, 31, iac, sb, 31, 0, 80, 0, 24, iac, se}); e != nil {
			return e
		}
		return send(c, []byte{iac, sb, 99, 1, iac, iac, 2, iac, se, 'o', 'k'})
	}, []byte("ok"))
}
func TestDisableAndReenableOptions(t *testing.T) {
	wire(t, func(c net.Conn) error {
		for _, step := range []struct{ input, output []byte }{{[]byte{iac, will, 3}, []byte{iac, do, 3}}, {[]byte{iac, wont, 3}, []byte{iac, dont, 3}}, {[]byte{iac, will, 3}, []byte{iac, do, 3}}, {[]byte{iac, do, 0}, []byte{iac, will, 0}}, {[]byte{iac, dont, 0}, []byte{iac, wont, 0}}} {
			if e := send(c, step.input); e != nil {
				return e
			}
			if e := expect(c, step.output); e != nil {
				return e
			}
		}
		return send(c, []byte("ok"))
	}, []byte("ok"))
}
func TestIncompleteControlDoesNotHoldDecodedOutput(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	go func() { _ = send(b, []byte{'o', 'k', iac}) }()
	c := NewConn(a, time.Second)
	done := make(chan error, 1)
	go func() {
		p := make([]byte, 16)
		n, e := c.Read(p)
		if e == nil && string(p[:n]) != "ok" {
			e = errors.New("incorrect partial output")
		}
		done <- e
	}()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("decoded output blocked by incomplete IAC")
	}
}
func TestMalformedAndOversizedSubnegotiation(t *testing.T) {
	for _, data := range [][]byte{{iac}, {iac, sb, 24, 1, iac, 42}, append([]byte{iac, sb, 99}, []byte(strings.Repeat("x", maxSubneg+1))...)} {
		a, b := net.Pipe()
		_ = a.SetReadDeadline(time.Now().Add(time.Second))
		go func() { defer b.Close(); _, _ = b.Write(data) }()
		_, e := io.ReadAll(NewConn(a, time.Second))
		_ = a.Close()
		if e == nil {
			t.Fatal("accepted malformed/unbounded sequence")
		}
	}
}
func TestWriteEscapesAndCountsOriginalBytes(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	done := make(chan error, 1)
	go func() { done <- expect(b, []byte{'x', iac, iac, '\r', '\n'}) }()
	n, e := NewConn(a, time.Second).Write([]byte{'x', iac, '\r', '\n'})
	if e != nil || n != 4 {
		t.Fatal(n, e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
}
func TestWriteDeadline(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	start := time.Now()
	_, e := NewConn(a, 30*time.Millisecond).Write([]byte("blocked"))
	if e == nil || time.Since(start) > time.Second {
		t.Fatal("write deadline not enforced", e)
	}
}
func TestNegotiationWriteDeadline(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	go func() { _, _ = b.Write([]byte{iac, will, 1}) }()
	start := time.Now()
	_, e := NewConn(a, 30*time.Millisecond).Read(make([]byte, 1))
	if e == nil || time.Since(start) > time.Second {
		t.Fatal("negotiation reply blocked", e)
	}
}
