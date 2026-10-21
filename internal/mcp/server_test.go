package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zytyan/sessmcp/internal/core"
	"github.com/zytyan/sessmcp/internal/testutil"
)

type client struct {
	in       *io.PipeWriter
	messages chan map[string]any
	pending  map[string]map[string]any
	done     chan error
}

func harness(t *testing.T) *client {
	t.Helper()
	r, w := io.Pipe()
	rr, ww := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	c := &client{in: w, messages: make(chan map[string]any, 100), pending: make(map[string]map[string]any), done: make(chan error, 1)}
	s := &Server{Manager: core.NewManager()}
	go func() { c.done <- s.Serve(ctx, r, ww); _ = ww.Close() }()
	go func() {
		defer close(c.messages)
		scan := bufio.NewScanner(rr)
		scan.Buffer(make([]byte, 4096), 1<<20)
		for scan.Scan() {
			var v map[string]any
			if json.Unmarshal(scan.Bytes(), &v) == nil {
				c.messages <- v
			}
		}
	}()
	t.Cleanup(func() {
		cancel()
		_ = w.Close()
		_ = r.Close()
		_ = rr.Close()
		_ = ww.Close()
		select {
		case <-c.done:
		case <-time.After(time.Second):
			t.Error("stdio server failed to stop")
		}
	})
	return c
}
func (c *client) send(t *testing.T, id, method string, params any) {
	t.Helper()
	v := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	if id != "" {
		v["id"] = id
	}
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.in.Write(append(b, '\n')); e != nil {
		t.Fatal(e)
	}
}
func (c *client) receive(t *testing.T, id string, d time.Duration) map[string]any {
	t.Helper()
	if v := c.pending[id]; v != nil {
		delete(c.pending, id)
		return v
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case v, ok := <-c.messages:
			if !ok {
				t.Fatal("stdio closed without response")
			}
			key, _ := v["id"].(string)
			if key == id {
				return v
			}
			c.pending[key] = v
		case <-timer.C:
			t.Fatalf("no response for %s within %s", id, d)
		}
	}
}
func (c *client) tool(t *testing.T, id, name string, args any) {
	t.Helper()
	c.send(t, id, "tools/call", map[string]any{"name": name, "arguments": args})
}
func structured(t *testing.T, v map[string]any) map[string]any {
	t.Helper()
	r, ok := v["result"].(map[string]any)
	if !ok {
		t.Fatalf("not a result: %+v", v)
	}
	s, ok := r["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("missing structured data: %+v", r)
	}
	return s
}
func TestStdioConcurrencyCancellationAndDevice(t *testing.T) {
	addr := testutil.TelnetSwitch(t)
	c := harness(t)
	c.send(t, "init", "initialize", map[string]any{"protocolVersion": "2025-11-25"})
	c.receive(t, "init", time.Second)
	c.send(t, "list", "tools/list", map[string]any{})
	v := c.receive(t, "list", time.Second)
	if len(v["result"].(map[string]any)["tools"].([]any)) != 6 {
		t.Fatal("tool discovery")
	}
	c.tool(t, "connect", "sess_connect", map[string]any{"protocol": "telnet", "address": addr})
	s := structured(t, c.receive(t, "connect", time.Second))
	id := s["session"].(string)
	c.tool(t, "greeting", "sess_read", map[string]any{"session": id, "wait": "100ms"})
	c.receive(t, "greeting", time.Second)
	c.tool(t, "read", "sess_read", map[string]any{"session": id, "wait": "30s", "waitfor": "IMPOSSIBLE"})
	c.send(t, "ping", "ping", map[string]any{})
	start := time.Now()
	c.receive(t, "ping", 500*time.Millisecond)
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("head of line blocking")
	}
	c.send(t, "", "notifications/cancelled", map[string]any{"requestId": "read", "reason": "test"})
	c.receive(t, "read", time.Second)
	c.tool(t, "command", "sess_interact", map[string]any{"session": id, "data": "show version\r\n", "waitfor": "switch> ", "wait": "1s"})
	s = structured(t, c.receive(t, "command", 2*time.Second))
	if !strings.Contains(s["output"].(string), "SwitchOS") {
		t.Fatal(s)
	}
	c.tool(t, "invalid", "sess_interact", map[string]any{"session": id, "data": "x", "extra": "wrong"})
	v = c.receive(t, "invalid", time.Second)
	if !v["result"].(map[string]any)["isError"].(bool) {
		t.Fatal("unknown argument accepted")
	}
}
func TestBlockedStdoutExits(t *testing.T) {
	r, w := io.Pipe()
	defer r.Close()
	defer w.Close()
	s := &Server{Manager: core.NewManager()}
	start := time.Now()
	done := make(chan error, 1)
	go func() {
		done <- s.Serve(context.Background(), strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\"}\n"), w)
	}()
	select {
	case e := <-done:
		if e == nil || time.Since(start) > 5*time.Second {
			t.Fatalf("blocked stdout: %v after %v", e, time.Since(start))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stdout deadlocked")
	}
}
func TestEOFDrainsFinalResponse(t *testing.T) {
	s := &Server{Manager: core.NewManager()}
	var out strings.Builder
	e := s.Serve(context.Background(), strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\"}\n"), &out)
	if e != nil || !strings.Contains(out.String(), `"result":{}`) {
		t.Fatal(e, out.String())
	}
}
func TestHalfOpenUnderTwoMinutes(t *testing.T) {
	if os.Getenv("SESSMCP_LONG_TEST") != "1" {
		t.Skip("set SESSMCP_LONG_TEST=1 for real 110s half-open E2E")
	}
	addr := testutil.Listen(t, func(conn net.Conn) {
		_, _ = io.Copy(io.Discard, conn) // Never emit bytes, keep the TCP connection open indefinitely.
	})
	c := harness(t)
	c.tool(t, "connect", "sess_connect", map[string]any{"protocol": "telnet", "address": addr})
	v := structured(t, c.receive(t, "connect", time.Second))
	start := time.Now()
	c.tool(t, "hang", "sess_read", map[string]any{"session": v["session"], "wait": "5min", "waitfor": "NEVER"})
	c.send(t, "ping", "ping", map[string]any{})
	c.receive(t, "ping", time.Second)
	r := c.receive(t, "hang", 118*time.Second)
	elapsed := time.Since(start)
	v = structured(t, r)
	if elapsed >= 120*time.Second || v["error"] == "" || v["connected"] != false {
		t.Fatalf("half-open safety failed after %s: %+v", elapsed, v)
	}
	t.Logf("half-open response returned through stdio in %s: %v", elapsed, v["error"])
}

func TestToolSchemas(t *testing.T) {
	for _, tool := range Tools() {
		b, e := json.Marshal(tool)
		if e != nil {
			t.Fatal(e)
		}
		var decoded map[string]any
		if e = json.Unmarshal(b, &decoded); e != nil {
			t.Fatal(e)
		}
		schema := decoded["inputSchema"].(map[string]any)
		if _, ok := schema["required"].([]any); !ok {
			t.Fatalf("invalid required array: %s", b)
		}
	}
}
