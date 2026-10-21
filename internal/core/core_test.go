package core

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/zytyan/sessmcp/internal/testutil"
)

func manager(t *testing.T) *Manager { t.Helper(); m := NewManager(); t.Cleanup(m.CloseAll); return m }
func run(t *testing.T, m *Manager, kind string, r Request) Result {
	t.Helper()
	v, e := m.Run(context.Background(), kind, r)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func TestSSHExitAndIncremental(t *testing.T) {
	addr, key := testutil.SSH(t)
	m := manager(t)
	v, e := m.Connect(context.Background(), Options{Protocol: "ssh", Address: addr, User: "test", Password: "test", HostKey: key})
	if e != nil {
		t.Fatal(e)
	}
	id := v.Session
	start := time.Now()
	v = run(t, m, "exec", Request{Session: id, Command: "both", Wait: "30s"})
	if v.Reason != "exit" || v.ExitCode == nil || *v.ExitCode != 0 || !strings.Contains(v.Output, "stdout") || !strings.Contains(v.Output, "stderr") || time.Since(start) > time.Second {
		t.Fatalf("SSH completion: %+v", v)
	}
	v = run(t, m, "exec", Request{Session: id, Command: "exit7"})
	if v.ExitCode == nil || *v.ExitCode != 7 {
		t.Fatalf("exit status: %+v", v)
	}
	v = run(t, m, "exec", Request{Session: id, Command: "slow", Wait: "30ms"})
	if !v.Running || !strings.Contains(v.Output, "first") {
		t.Fatalf("partial: %+v", v)
	}
	busy := run(t, m, "exec", Request{Session: id, Command: "both"})
	if busy.Reason != "busy" {
		t.Fatalf("expected busy: %+v", busy)
	}
	v = run(t, m, "read", Request{Session: id, Wait: "1s"})
	if v.Running || !strings.Contains(v.Output, "second") || strings.Contains(v.Output, "first") {
		t.Fatalf("delta: %+v", v)
	}
	v = run(t, m, "read", Request{Session: id, All: true})
	if !strings.Contains(v.Output, "first") || !strings.Contains(v.Output, "second") {
		t.Fatalf("history: %+v", v)
	}
}
func TestHostVerification(t *testing.T) {
	addr, _ := testutil.SSH(t)
	m := manager(t)
	_, e := m.Connect(context.Background(), Options{Protocol: "ssh", Address: addr, User: "x", Password: "x", HostKey: "SHA256:bad"})
	if e == nil {
		t.Fatal("host key mismatch accepted")
	}
}
func TestDeviceSimulators(t *testing.T) {
	for _, protocol := range []string{"ssh", "telnet"} {
		t.Run(protocol, func(t *testing.T) {
			m := manager(t)
			o := Options{Protocol: protocol, Interactive: true, User: "test", Password: "test"}
			if protocol == "ssh" {
				o.Address, o.HostKey = testutil.SSH(t)
			} else {
				o.Address = testutil.TelnetSwitch(t)
			}
			v, e := m.Connect(context.Background(), o)
			if e != nil {
				t.Fatal(e)
			}
			id := v.Session
			// Connect may include the greeting; wait for it only if not already returned.
			if !strings.Contains(v.Output, "switch> ") {
				v = run(t, m, "read", Request{Session: id, WaitFor: `switch> `, Wait: "1s"})
				if !strings.Contains(v.Output, "switch> ") {
					t.Fatalf("greeting: %+v", v)
				}
			}
			for _, step := range []struct{ data, pattern string }{{"enable\r\n", `switch# `}, {"configure terminal\r\n", `switch\(config\)# `}, {"interface ethernet 1\r\n", `switch\(config-if\)# `}, {"end\r\n", `switch# `}, {"show running-config\r\n", `--More--`}, {" ", `switch# `}, {"slow\r\n", `switch# `}} {
				v = run(t, m, "interact", Request{Session: id, Data: step.data, WaitFor: step.pattern, Timeout: "500ms", Wait: "2s"})
				if v.Reason != "waitfor" {
					t.Fatalf("%q: %+v", step.data, v)
				}
			}
			v = run(t, m, "interact", Request{Session: id, Data: strings.Repeat("x", InputLimit+1)})
			if !v.Backpressure || v.Reason != "backpressure" {
				t.Fatalf("unbounded input accepted: %+v", v)
			}
			v = run(t, m, "interact", Request{Session: id, Data: "quit\r\n", Wait: "1s"})
			if v.Connected || v.Error == "" || !strings.Contains(v.Hint, "connect") {
				t.Fatalf("disconnect: %+v", v)
			}
		})
	}
}
func TestWaitConditionsAndBusy(t *testing.T) {
	addr := testutil.Listen(t, func(c net.Conn) {
		_, _ = c.Write([]byte("hello"))
		b := make([]byte, 32)
		for {
			n, e := c.Read(b)
			if e != nil {
				return
			}
			if string(b[:n]) == "tick" {
				for i := 0; i < 10; i++ {
					_, _ = c.Write([]byte("x"))
					time.Sleep(20 * time.Millisecond)
				}
			}
		}
	})
	m := manager(t)
	v, e := m.Connect(context.Background(), Options{Protocol: "telnet", Address: addr})
	if e != nil {
		t.Fatal(e)
	}
	id := v.Session
	v = run(t, m, "read", Request{Session: id, Wait: "60ms", WaitFor: "NEVER", Timeout: "1s"})
	if v.Reason != "wait" {
		t.Fatalf("fixed: %+v", v)
	}
	start := time.Now()
	v = run(t, m, "interact", Request{Session: id, Data: "tick", Wait: "1s", Timeout: "50ms"})
	if v.Reason != "timeout" || time.Since(start) < 200*time.Millisecond || time.Since(start) > 800*time.Millisecond {
		t.Fatalf("idle reset: %+v", v)
	}
	done := make(chan Result, 1)
	go func() { r, _ := m.Run(context.Background(), "read", Request{Session: id, Wait: "1s"}); done <- r }()
	s, _ := m.Get(id)
	deadline := time.Now().Add(time.Second)
	for {
		if !s.op.TryLock() {
			break
		}
		s.op.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("operation never started")
		}
		time.Sleep(time.Millisecond)
	}
	v = run(t, m, "read", Request{Session: id})
	if v.Reason != "busy" || !v.Backpressure {
		t.Fatalf("busy: %+v", v)
	}
	_, _ = m.Close(id)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("close waited for operation lock")
	}
}
func TestBoundedHistoryAndTail(t *testing.T) {
	s := &Session{id: "test", connected: true, notify: make(chan struct{}, 1)}
	_, _ = s.Write([]byte(strings.Repeat("x", HistoryLimit+100)))
	v := s.snapshot(Request{}, "test")
	if !v.Truncated || !v.More || len(v.Output) != ResultLimit || v.RetainedFrom != 100 {
		t.Fatalf("history bounds: %+v", v.statusSummary())
	}
	off := v.End
	next := s.snapshot(Request{Offset: &off}, "test")
	if next.Start != off {
		t.Fatal("paging did not advance")
	}
	_, _ = s.Write([]byte("\none\ntwo\nthree\n"))
	v = s.snapshot(Request{Tail: 2}, "test")
	if v.Output != "two\nthree\n" {
		t.Fatalf("tail %q", v.Output)
	}
}
func (r Result) statusSummary() string { return r.Reason }
func TestValidation(t *testing.T) {
	for _, r := range []Request{{Wait: "0s"}, {Wait: "-1s"}, {Wait: "forever"}, {Timeout: "0s"}, {WaitFor: "["}, {Tail: -1}} {
		if _, e := parseWait(r); e == nil {
			t.Fatalf("accepted %+v", r)
		}
	}
	if d, e := Duration("1min"); e != nil || d != time.Minute {
		t.Fatal(d, e)
	}
}

func TestInputWriteDeadlineAndBackpressure(t *testing.T) {
	t.Run("blocked write bounded by fixed wait", func(t *testing.T) {
		m := manager(t)
		a, b := net.Pipe()
		defer b.Close()
		s := &Session{id: "blocked", interactive: true, connected: true, last: time.Now(), notify: make(chan struct{}, 1), done: make(chan struct{}), input: a, closer: a}
		m.sessions[s.id] = s
		start := time.Now()
		v := run(t, m, "interact", Request{Session: s.id, Data: "x", Wait: "30ms"})
		if v.Connected || v.Reason != "io_error" || time.Since(start) > time.Second {
			t.Fatalf("blocked write escaped deadline: %+v", v)
		}
	})
	t.Run("unacknowledged input rejected", func(t *testing.T) {
		addr := testutil.Listen(t, func(c net.Conn) {
			b := make([]byte, 32)
			for {
				if _, e := c.Read(b); e != nil {
					return
				}
			}
		})
		m := manager(t)
		v, e := m.Connect(context.Background(), Options{Protocol: "telnet", Address: addr})
		if e != nil {
			t.Fatal(e)
		}
		id := v.Session
		v = run(t, m, "interact", Request{Session: id, Data: "first", Wait: "20ms"})
		if !v.Backpressure || !v.Connected {
			t.Fatal(v)
		}
		v = run(t, m, "interact", Request{Session: id, Data: "second", Wait: "20ms"})
		if v.Reason != "backpressure" {
			t.Fatalf("queued more data: %+v", v)
		}
		v = run(t, m, "interact", Request{Session: id, Data: "\x03", Wait: "20ms"})
		if v.Reason == "backpressure" {
			t.Fatal("Ctrl-C rejected")
		}
	})
}
