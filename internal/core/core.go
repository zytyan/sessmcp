// Package core owns bounded, process-local remote sessions.
package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	HistoryLimit = 1 << 20
	ResultLimit  = 64 << 10
	InputLimit   = 4096
	IdleLimit    = 110 * time.Second
	CallLimit    = 115 * time.Second
	IOLimit      = 10 * time.Second
	MaxSessions  = 32
)

type Options struct {
	Protocol      string `json:"protocol"`
	Address       string `json:"address"`
	User          string `json:"user,omitempty"`
	Password      string `json:"password,omitempty"`
	KeyFile       string `json:"key_file,omitempty"`
	KeyPassphrase string `json:"key_passphrase,omitempty"`
	KnownHosts    string `json:"known_hosts,omitempty"`
	HostKey       string `json:"host_key,omitempty"`
	Insecure      bool   `json:"insecure,omitempty"`
	Interactive   bool   `json:"interactive,omitempty"`
}
type Request struct {
	Session string `json:"session"`
	Command string `json:"command,omitempty"`
	Data    string `json:"data,omitempty"`
	Wait    string `json:"wait,omitempty"`
	WaitFor string `json:"waitfor,omitempty"`
	Timeout string `json:"timeout,omitempty"`
	All     bool   `json:"all,omitempty"`
	Tail    int    `json:"tail,omitempty"`
	Offset  *int64 `json:"offset,omitempty"`
}
type Result struct {
	Session      string `json:"session"`
	Output       string `json:"output"`
	Reason       string `json:"reason"`
	Connected    bool   `json:"connected"`
	Running      bool   `json:"running"`
	ExitCode     *int   `json:"exit_code,omitempty"`
	Error        string `json:"error,omitempty"`
	Hint         string `json:"hint,omitempty"`
	Start        int64  `json:"start"`
	End          int64  `json:"end"`
	Total        int64  `json:"total"`
	RetainedFrom int64  `json:"retained_from"`
	More         bool   `json:"more"`
	Truncated    bool   `json:"truncated"`
	Backpressure bool   `json:"backpressure"`
}
type Session struct {
	awaiting            bool
	sentAt              int64
	id                  string
	interactive         bool
	mu                  sync.Mutex
	op                  sync.Mutex
	buf                 []byte
	base, total, cursor int64
	last                time.Time
	connected, running  bool
	failure             string
	exit                *int
	notify              chan struct{}
	done                chan struct{}
	once                sync.Once
	closer              io.Closer
	input               io.Writer
	exec                func(string) error
}
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
	opening  int
}

func NewManager() *Manager { return &Manager{sessions: make(map[string]*Session)} }
func (m *Manager) Get(id string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[id]
	if s == nil {
		return nil, errors.New("unknown session; reconnect using connect")
	}
	return s, nil
}
func (m *Manager) Connect(ctx context.Context, o Options) (Result, error) {
	m.mu.Lock()
	if len(m.sessions)+m.opening >= MaxSessions {
		m.mu.Unlock()
		return Result{}, errors.New("session limit reached; close unused sessions")
	}
	m.opening++
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.opening--; m.mu.Unlock() }()
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return Result{}, e
	}
	s := &Session{id: hex.EncodeToString(b), interactive: o.Interactive || o.Protocol == "telnet", last: time.Now(), connected: true, notify: make(chan struct{}, 1), done: make(chan struct{})}
	var e error
	switch o.Protocol {
	case "ssh":
		e = s.connectSSH(ctx, o)
	case "telnet":
		e = s.connectTelnet(ctx, o)
	default:
		e = errors.New("protocol must be ssh or telnet")
	}
	if e != nil {
		s.disconnect(e.Error())
		return Result{}, e
	}
	m.mu.Lock()
	m.sessions[s.id] = s
	m.mu.Unlock()
	go s.watch()
	r := s.snapshot(Request{}, "connected")
	r.Hint = "Standard SSH servers: prefer exec (interactive=false); SSH exit-status returns immediately. For device shells use interact with waitfor/timeout."
	return r, nil
}
func (s *Session) signal() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}
func (s *Session) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last = time.Now()
	n := len(p)
	s.total += int64(n)
	if n >= HistoryLimit {
		s.buf = append(s.buf[:0], p[n-HistoryLimit:]...)
	} else {
		s.buf = append(s.buf, p...)
		if len(s.buf) > HistoryLimit {
			s.buf = s.buf[len(s.buf)-HistoryLimit:]
		}
	}
	s.base = s.total - int64(len(s.buf))
	s.signal()
	return n, nil
}
func (s *Session) disconnect(why string) {
	s.once.Do(func() {
		s.mu.Lock()
		s.connected = false
		s.running = false
		s.failure = why
		c := s.closer
		s.mu.Unlock()
		close(s.done)
		s.signal()
		if c != nil {
			_ = c.Close()
		}
	})
}
func (s *Session) watch() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
			s.mu.Lock()
			stale := (s.running || s.interactive) && time.Since(s.last) >= IdleLimit
			s.mu.Unlock()
			if stale {
				s.disconnect("endpoint_idle: no stdout/stderr for 110s; reconnect")
				return
			}
		}
	}
}
func (m *Manager) Close(id string) (Result, error) {
	s, e := m.Get(id)
	if e != nil {
		return Result{}, e
	}
	s.disconnect("closed by caller")
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
	return s.snapshot(Request{}, "closed"), nil
}
func (m *Manager) CloseAll() {
	m.mu.Lock()
	ss := m.sessions
	m.sessions = make(map[string]*Session)
	m.mu.Unlock()
	for _, s := range ss {
		s.disconnect("server shutdown")
	}
}
func (m *Manager) List() []Result {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Result, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s.status())
	}
	return out
}
func (s *Session) status() Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Result{Session: s.id, Connected: s.connected, Running: s.running, Error: s.failure, Total: s.total}
}
func Duration(v string) (time.Duration, error) {
	v = strings.ReplaceAll(v, "min", "m")
	d, e := time.ParseDuration(v)
	if e != nil || d <= 0 {
		return 0, fmt.Errorf("invalid positive duration %q (examples: 100ms, 30s, 1min)", v)
	}
	return d, nil
}

type waits struct {
	fixed, idle time.Duration
	re          *regexp.Regexp
}

func parseWait(r Request) (w waits, e error) {
	if r.Wait == "" {
		w.fixed = 5 * time.Second
	} else {
		w.fixed, e = Duration(r.Wait)
		if e != nil {
			return w, e
		}
	}
	if r.Timeout != "" {
		w.idle, e = Duration(r.Timeout)
		if e != nil {
			return w, e
		}
	}
	if len(r.WaitFor) > 1024 {
		return w, errors.New("waitfor too long")
	}
	if r.WaitFor != "" {
		w.re, e = regexp.Compile(r.WaitFor)
	}
	if r.Tail < 0 || r.Tail > 100000 {
		return w, errors.New("tail must be 0..100000")
	}
	if r.Offset != nil && *r.Offset < 0 {
		return w, errors.New("offset must be nonnegative")
	}
	return w, e
}
func (m *Manager) Run(ctx context.Context, kind string, r Request) (Result, error) {
	started := time.Now()
	w, e := parseWait(r)
	if e != nil {
		return Result{}, e
	}
	s, e := m.Get(r.Session)
	if e != nil {
		return Result{}, e
	}
	if !s.op.TryLock() {
		v := s.status()
		v.Reason = "busy"
		v.Backpressure = true
		v.Hint = "Another call owns this session; wait for it, or close the session. Do not queue commands."
		return v, nil
	}
	defer s.op.Unlock()
	if kind == "read" && (r.Command != "" || r.Data != "") {
		return Result{}, errors.New("read does not accept input")
	}
	if kind == "exec" && s.interactive {
		return Result{}, errors.New("exec requires non-interactive SSH; use interact for this session")
	}
	if kind == "interact" && !s.interactive {
		return Result{}, errors.New("use exec on standard SSH, or reconnect with interactive=true")
	}
	if (kind == "exec" && r.Data != "") || (kind == "interact" && r.Command != "") {
		return Result{}, errors.New("exec accepts command; interact accepts exact data (include newline yourself)")
	}
	input := r.Data
	if kind == "exec" {
		input = r.Command
		if input == "" {
			return Result{}, errors.New("command is required")
		}
	}
	if len(input) > InputLimit {
		v := s.status()
		v.Reason = "backpressure"
		v.Backpressure = true
		v.Hint = "Input rejected (maximum 4096 bytes). Send a small command, await prompt/echo, then continue. Never paste bulk data to a slow serial endpoint."
		return v, nil
	}
	s.mu.Lock()
	matchStart := s.total
	if input == "" {
		matchStart = s.cursor
	}
	connected := s.connected
	running := s.running
	s.mu.Unlock()
	if !connected {
		return s.snapshot(r, "disconnected"), nil
	}
	if kind == "exec" && running {
		v := s.snapshot(r, "busy")
		v.Backpressure = true
		v.Hint = "Previous SSH command is still running. Use read; do not replay it."
		return v, nil
	}
	if input != "" {
		s.mu.Lock()
		control := len(input) == 1 && (input[0] == 3 || input[0] == 4 || input[0] == 27)
		if s.interactive && s.awaiting && s.total <= s.sentAt && !control {
			s.mu.Unlock()
			v := s.snapshot(r, "backpressure")
			v.Backpressure = true
			v.Hint = "Previous input has produced no output yet. Poll sess_read before sending more; Ctrl-C/Ctrl-D/Escape remain available. Avoid accumulating serial input."
			return v, nil
		}
		if s.interactive {
			s.awaiting = true
			s.sentAt = s.total
		}
		s.running = true
		if kind == "exec" {
			s.exit = nil
			s.last = started
		}
		s.mu.Unlock()
		errc := make(chan error, 1)
		go func() {
			if kind == "exec" {
				errc <- s.exec(input)
			} else {
				_, err := s.input.Write([]byte(input))
				errc <- err
			}
		}()
		budget := IOLimit
		if left := w.fixed - time.Since(started); left < budget {
			budget = left
		}
		timer := time.NewTimer(budget)
		select {
		case e = <-errc:
		case <-timer.C:
			e = errors.New("input/setup timed out; partial input may have executed; endpoint may be slow; reconnect and inspect state before retrying")
		case <-ctx.Done():
			e = ctx.Err()
		}
		timer.Stop()
		if e != nil {
			s.disconnect(e.Error())
			return s.snapshot(r, "io_error"), nil
		}
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		s.mu.Lock()
		last := s.last
		conn := s.connected
		run := s.running
		from := matchStart
		if from < s.base {
			from = s.base
		}
		var match bool
		if w.re != nil {
			match = w.re.Match(s.buf[from-s.base:])
		}
		s.mu.Unlock()
		reason := ""
		now := time.Now()
		switch {
		case !conn:
			reason = "disconnected"
		case !s.interactive && !run:
			reason = "exit"
		case now.Sub(last) >= IdleLimit:
			s.disconnect("endpoint_idle: no stdout/stderr for 110s; reconnect")
			reason = "endpoint_idle"
		case match:
			reason = "waitfor"
		case now.Sub(started) >= CallLimit:
			reason = "call_deadline"
		case now.Sub(started) >= w.fixed:
			reason = "wait"
		case w.idle > 0 && now.Sub(last) >= w.idle && now.Sub(started) >= w.idle:
			reason = "timeout"
		}
		if reason != "" {
			if reason == "waitfor" && s.interactive {
				s.mu.Lock()
				s.running = false
				s.awaiting = false
				s.mu.Unlock()
			}
			return s.snapshot(r, reason), nil
		}
		select {
		case <-ctx.Done():
			return s.snapshot(r, "cancelled"), nil
		case <-s.notify:
		case <-ticker.C:
		}
	}
}
func (s *Session) snapshot(r Request, reason string) Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	from := s.cursor
	if r.All {
		from = s.base
	}
	if r.Offset != nil {
		from = *r.Offset
	}
	if r.Tail > 0 {
		end := len(s.buf)
		if end > 0 && s.buf[end-1] == '\n' {
			end--
		}
		n := 0
		pos := end
		for pos > 0 {
			pos--
			if s.buf[pos] == '\n' {
				n++
				if n == r.Tail {
					pos++
					break
				}
			}
		}
		from = s.base + int64(pos)
	}
	lost := from < s.base
	if from < s.base {
		from = s.base
	}
	if from > s.total {
		from = s.total
	}
	end := s.total
	if end-from > ResultLimit {
		end = from + ResultLimit
	}
	out := string(s.buf[from-s.base : end-s.base])
	if !r.All && r.Tail == 0 && r.Offset == nil {
		s.cursor = end
	}
	v := Result{Session: s.id, Output: out, Reason: reason, Connected: s.connected, Running: s.running, ExitCode: s.exit, Error: s.failure, Start: from, End: end, Total: s.total, RetainedFrom: s.base, More: end < s.total, Truncated: lost || (r.All && s.base > 0)}
	if !s.connected {
		v.Hint = "Connection closed. Call connect to create a new session; do not blindly replay commands."
	}
	if v.More || v.Truncated {
		v.Backpressure = true
		v.Hint += " Output bounded: fetch more using read with offset=end (or default incremental read). Reduce remote verbosity."
	}
	if s.interactive && s.running {
		v.Backpressure = true
		v.Hint += " Slow terminal: send at most one small command per prompt; avoid bulk input."
	}
	return v
}
