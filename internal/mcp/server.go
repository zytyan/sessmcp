// Package mcp implements the newline-delimited JSON-RPC stdio binding.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/zytyan/sessmcp/internal/core"
)

const MaxInflight = 32
const WriteLimit = 3 * time.Second

type Audit interface{ Record(any) error }
type Server struct {
	Manager *core.Manager
	Audit   Audit
}
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func strict(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
func toolResult(v any, e error) any {
	bad := e != nil
	if e != nil {
		v = map[string]any{"error": e.Error()}
	}
	if r, ok := v.(core.Result); ok && r.Error != "" {
		bad = true
	}
	b, _ := json.Marshal(v)
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": string(b)}}, "structuredContent": v, "isError": bad}
}
func (s *Server) call(ctx context.Context, p json.RawMessage) any {
	var args struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Meta      json.RawMessage `json:"_meta,omitempty"`
	}
	if e := strict(p, &args); e != nil {
		return toolResult(nil, e)
	}
	if len(args.Arguments) == 0 {
		args.Arguments = json.RawMessage(`{}`)
	}
	if s.Audit != nil {
		var record any
		_ = json.Unmarshal(p, &record)
		if e := s.Audit.Record(record); e != nil {
			return toolResult(nil, fmt.Errorf("audit unavailable: %w", e))
		}
	}
	switch args.Name {
	case "sess_connect":
		var o core.Options
		if e := strict(args.Arguments, &o); e != nil {
			return toolResult(nil, e)
		}
		v, e := s.Manager.Connect(ctx, o)
		return toolResult(v, e)
	case "sess_exec", "sess_interact", "sess_read":
		var r core.Request
		if e := strict(args.Arguments, &r); e != nil {
			return toolResult(nil, e)
		}
		kind := map[string]string{"sess_exec": "exec", "sess_interact": "interact", "sess_read": "read"}[args.Name]
		v, e := s.Manager.Run(ctx, kind, r)
		return toolResult(v, e)
	case "sess_close":
		var r struct {
			Session string `json:"session"`
		}
		if e := strict(args.Arguments, &r); e != nil {
			return toolResult(nil, e)
		}
		v, e := s.Manager.Close(r.Session)
		return toolResult(v, e)
	case "sess_list":
		var r struct{}
		if e := strict(args.Arguments, &r); e != nil {
			return toolResult(nil, e)
		}
		return toolResult(map[string]any{"sessions": s.Manager.List()}, nil)
	default:
		return toolResult(nil, errors.New("unknown tool"))
	}
}
func schema(props map[string]any, required ...string) any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}
func str(desc string) any { return map[string]any{"type": "string", "description": desc} }
func Tools() []any {
	connect := map[string]any{"protocol": map[string]any{"type": "string", "enum": []string{"ssh", "telnet"}}, "address": str("host:port; IPv6 [host]:port"), "user": str("SSH username"), "password": str("SSH password; redacted in audit"), "key_file": str("Local SSH private key path"), "key_passphrase": str("Private key passphrase; redacted in audit"), "known_hosts": str("Local known_hosts path"), "host_key": str("Pinned SHA256 host-key fingerprint"), "insecure": map[string]any{"type": "boolean", "description": "Explicitly skip host-key verification (lab only)"}, "interactive": map[string]any{"type": "boolean", "description": "Default false: standard SSH MUST prefer exec for immediate exit-status. True for switch/router foreground shells."}}
	out := []any{map[string]any{"name": "sess_connect", "description": "Create persistent SSH/Telnet connection. Prefer non-interactive SSH exec on standard servers. Telnet is plaintext; use on trusted networks. No automatic reconnect or command replay.", "inputSchema": schema(connect, "protocol", "address")}}
	for _, kind := range []string{"exec", "interact", "read"} {
		p := map[string]any{"session": str("Handle returned by sess_connect"), "wait": str("Fixed time from call start; default 5s. Examples 100ms,30s,1min. Can exceed 115s but hard call cap is 115s."), "waitfor": str("Go RE2 regex matched against new combined stdout/stderr; OR with wait/timeout/SSH exit"), "timeout": str("Time since last stdout/stderr refresh, not since last input (e.g. 100ms). OR with wait/waitfor."), "all": map[string]any{"type": "boolean", "description": "Read retained history, at most 64KiB per result; use offset=end to paginate"}, "tail": map[string]any{"type": "integer", "minimum": 0, "maximum": 100000, "description": "Last N retained lines (64KiB response cap)"}, "offset": map[string]any{"type": "integer", "minimum": 0, "description": "Absolute byte offset for paging; dropped history is reported"}}
		desc := "Wait/read without sending input; default incremental output. Check more/truncated/end, connected/error, running/exit_code. Hard deadline 115s; endpoint idle 110s disconnects."
		req := []string{"session"}
		if kind == "exec" {
			p["command"] = str("SSH exec command, max 4096 bytes; new SSH channel on persistent transport. Shell state does not persist across exec calls.")
			req = append(req, "command")
			desc = "Preferred for standard SSH: start command and return immediately on SSH exit-status (combined stdout/stderr); OR wait/waitfor/timeout. If running=true, poll sess_read, never resubmit the command."
		}
		if kind == "interact" {
			p["data"] = str("Exact input, max 4096 bytes; include \\r\\n or \\n for Enter. Send one small command per prompt; empty data polls.")
			desc = "Send to persistent Telnet or interactive SSH shell. Use waitfor prompt/pager or timeout for silent terminals; any wait condition returns. Respect backpressure; never queue bulk input."
		}
		out = append(out, map[string]any{"name": "sess_" + kind, "description": desc, "inputSchema": schema(p, req...)})
	}
	out = append(out, map[string]any{"name": "sess_list", "description": "List sessions; detects disconnects. Reconnect with sess_connect when connected=false.", "inputSchema": schema(map[string]any{})}, map[string]any{"name": "sess_close", "description": "Close and release session, including a blocked operation. Does not wait for the session operation lock.", "inputSchema": schema(map[string]any{"session": str("Session handle")}, "session")})
	return out
}

// Serve never executes remote operations on its input dispatcher. One bounded
// output writer owns stdout; a blocked consumer ends Serve within WriteLimit.
// A stream cannot deliver past a non-reading peer, so the process must exit.
func (s *Server) Serve(parent context.Context, in io.Reader, out io.Writer) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	defer s.Manager.CloseAll()
	type line struct {
		b   []byte
		err error
	}
	incoming := make(chan line, 1)
	go func() {
		scan := bufio.NewScanner(in)
		scan.Buffer(make([]byte, 4096), 128<<10)
		for scan.Scan() {
			b := append([]byte(nil), scan.Bytes()...)
			select {
			case incoming <- line{b: b}:
			case <-ctx.Done():
				return
			}
		}
		e := scan.Err()
		if e == nil {
			e = io.EOF
		}
		select {
		case incoming <- line{err: e}:
		case <-ctx.Done():
		}
	}()
	type output struct {
		b   []byte
		ack chan struct{}
	}
	queue := make(chan output, 64)
	writerErr := make(chan error, 1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case item := <-queue:
				if item.ack != nil {
					close(item.ack)
					continue
				}
				b := item.b
				done := make(chan error, 1)
				go func() {
					for len(b) > 0 {
						n, e := out.Write(b)
						if e != nil {
							done <- e
							return
						}
						if n == 0 {
							done <- io.ErrShortWrite
							return
						}
						b = b[n:]
					}
					done <- nil
				}()
				timer := time.NewTimer(WriteLimit)
				select {
				case e := <-done:
					timer.Stop()
					if e != nil {
						writerErr <- e
						return
					}
				case <-timer.C:
					writerErr <- errors.New("stdio output blocked for 3s; terminating instead of blocking request dispatcher")
					return
				case <-ctx.Done():
					timer.Stop()
					return
				}
			}
		}
	}()
	send := func(id json.RawMessage, result any, code int, msg string) {
		v := map[string]any{"jsonrpc": "2.0", "id": id}
		if code != 0 {
			v["error"] = map[string]any{"code": code, "message": msg}
		} else {
			v["result"] = result
		}
		b, e := json.Marshal(v)
		if e != nil {
			cancel()
			return
		}
		b = append(b, '\n')
		select {
		case queue <- output{b: b}:
		default:
			cancel()
		}
	}
	var wg sync.WaitGroup
	slots := make(chan struct{}, MaxInflight)
	var mu sync.Mutex
	active := make(map[string]context.CancelFunc)
	var eofDone chan struct{}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case e := <-writerErr:
			return e
		case <-eofDone:
			ack := make(chan struct{})
			timer := time.NewTimer(WriteLimit + time.Second)
			select {
			case queue <- output{ack: ack}:
			case e := <-writerErr:
				timer.Stop()
				return e
			case <-timer.C:
				return errors.New("stdio drain deadline")
			}
			select {
			case <-ack:
				timer.Stop()
				return nil
			case e := <-writerErr:
				timer.Stop()
				return e
			case <-timer.C:
				return errors.New("stdio drain deadline")
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			}

		case l := <-incoming:
			if l.err != nil {
				if l.err != io.EOF {
					return l.err
				}
				incoming = nil
				eofDone = make(chan struct{})
				go func() { wg.Wait(); close(eofDone) }()
				continue
			}
			var m message
			if e := json.Unmarshal(l.b, &m); e != nil {
				send(json.RawMessage("null"), nil, -32700, "parse error")
				continue
			}
			if m.JSONRPC != "2.0" || m.Method == "" {
				send(json.RawMessage("null"), nil, -32600, "invalid request")
				continue
			}
			if len(m.ID) == 0 {
				if m.Method == "notifications/cancelled" {
					var p struct {
						RequestID json.RawMessage `json:"requestId"`
					}
					if json.Unmarshal(m.Params, &p) == nil {
						mu.Lock()
						c := active[string(p.RequestID)]
						mu.Unlock()
						if c != nil {
							c()
						}
					}
				}
				continue
			}
			var id any
			if json.Unmarshal(m.ID, &id) != nil {
				send(json.RawMessage("null"), nil, -32600, "invalid id")
				continue
			}
			switch id.(type) {
			case string, float64:
			default:
				send(json.RawMessage("null"), nil, -32600, "id must be string or number")
				continue
			}
			switch m.Method {
			case "initialize":
				var p struct {
					ProtocolVersion string `json:"protocolVersion"`
				}
				_ = json.Unmarshal(m.Params, &p)
				version := p.ProtocolVersion
				switch version {
				case "2024-11-05", "2025-03-26", "2025-06-18", "2025-11-25":
				default:
					version = "2025-11-25"
				}
				send(m.ID, map[string]any{"protocolVersion": version, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "sessmcp", "version": "0.1.0"}, "instructions": "Prefer sess_exec for standard SSH. Device consoles: sess_interact with waitfor/timeout. Never blindly retry side-effecting commands. See backpressure/connected/more in every result."}, 0, "")
			case "ping":
				send(m.ID, map[string]any{}, 0, "")
			case "tools/list":
				send(m.ID, map[string]any{"tools": Tools()}, 0, "")
			case "tools/call":
				mu.Lock()
				_, dup := active[string(m.ID)]
				mu.Unlock()
				if dup {
					send(m.ID, nil, -32600, "duplicate active request id")
					continue
				}
				select {
				case slots <- struct{}{}:
				default:
					send(m.ID, toolResult(nil, errors.New("server busy: maximum 32 concurrent calls; reduce request rate")), 0, "")
					continue
				}
				workCtx, stop := context.WithTimeout(ctx, core.CallLimit)
				mu.Lock()
				active[string(m.ID)] = stop
				mu.Unlock()
				wg.Add(1)
				go func(m message, workCtx context.Context, stop context.CancelFunc) {
					defer wg.Done()
					defer func() { stop(); mu.Lock(); delete(active, string(m.ID)); mu.Unlock() }()
					result := make(chan any, 1)
					// A stuck local filesystem/library operation cannot hold up its RPC.
					// Its worker slot remains reserved until it actually finishes.
					go func() { defer func() { <-slots }(); result <- s.call(workCtx, m.Params) }()
					select {
					case v := <-result:
						send(m.ID, v, 0, "")
					case <-workCtx.Done():
						select {
						case v := <-result:
							send(m.ID, v, 0, "")
						case <-time.After(50 * time.Millisecond):
							send(m.ID, toolResult(nil, fmt.Errorf("call cancelled or hard deadline reached: %w; remote command may still be running; inspect session before retrying", workCtx.Err())), 0, "")
						}
					}
				}(m, workCtx, stop)

			default:
				send(m.ID, nil, -32601, "method not found")
			}
		}
	}
}
