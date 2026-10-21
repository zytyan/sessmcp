package cli

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/zytyan/sessmcp/internal/core"
	"github.com/zytyan/sessmcp/internal/mcp"
)

//go:embed skill.md
var Skill string

const Help = `sessmcp — 面向 AI Agent 的持久 SSH / Telnet MCP 工具 (Go 1.22.1+)

用法:
  sessmcp help                         查看用途和子命令
  sessmcp serve [--log PATH]           显式启动 MCP stdio 服务
  sessmcp mcp-skill                    输出 AI 操作指南
  sessmcp install-skill [--dir PATH]   安装 SKILL.md (默认 ./sessmcp-skill)
  sessmcp version                     查看版本

普通 SSH 优先 sess_exec，以 SSH 退出状态返回；交换机/路由器使用
sess_interact 和 waitfor 正则、wait 固定时间、timeout 输出静默时间。
等待条件任一满足即返回，默认 wait=5s，单次上限 115s，端点静默 110s 报错断开。
默认增量输出；all/tail/offset 可读取保留历史。慢端点返回反压提示。
单条交互命令优先 line（SSH 默认 CR）；空输出先 sess_read，不要重发。
MCP 配置: {"command":"/path/to/sessmcp","args":["serve"]}
--log 写入 0600 JSONL 审计；认证字段脱敏，命令/line/data 仍可能含敏感信息。
Telnet 为明文；SSH 默认验证主机密钥。连接只在本次服务进程内持久化。
`

func Run(ctx context.Context, args []string, in io.Reader, out, errout io.Writer) error {
	if len(args) == 0 {
		_, e := io.WriteString(out, Help)
		return e
	}
	switch args[0] {
	case "help", "--help", "-h":
		_, e := io.WriteString(out, Help)
		return e
	case "version":
		_, e := fmt.Fprintln(out, "sessmcp 0.1.0")
		return e
	case "mcp-skill":
		_, e := io.WriteString(out, Skill)
		return e
	case "install-skill":
		f := flag.NewFlagSet("install-skill", flag.ContinueOnError)
		f.SetOutput(errout)
		dir := f.String("dir", "sessmcp-skill", "Skill destination directory")
		if e := f.Parse(args[1:]); e != nil {
			return e
		}
		if f.NArg() != 0 {
			return errors.New("unexpected arguments")
		}
		if e := os.MkdirAll(*dir, 0755); e != nil {
			return e
		}
		p := filepath.Join(*dir, "SKILL.md")
		w, e := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if e != nil {
			return e
		}
		_, e = io.WriteString(w, Skill)
		ce := w.Close()
		if e != nil {
			return e
		}
		if ce != nil {
			return ce
		}
		_, e = fmt.Fprintln(out, p)
		return e
	case "serve":
		f := flag.NewFlagSet("serve", flag.ContinueOnError)
		f.SetOutput(errout)
		path := f.String("log", "", "Audit JSONL path (authentication redacted)")
		if e := f.Parse(args[1:]); e != nil {
			return e
		}
		if f.NArg() != 0 {
			return errors.New("unexpected arguments")
		}
		s := &mcp.Server{Manager: core.NewManager()}
		if *path != "" {
			a, e := OpenAudit(*path)
			if e != nil {
				return e
			}
			defer a.Close()
			s.Audit = a
		}
		return s.Serve(ctx, in, out)
	default:
		return fmt.Errorf("unknown subcommand %q; run sessmcp help", args[0])
	}
}

type auditEntry struct {
	b   []byte
	ack chan error
}
type Logger struct {
	file    *os.File
	queue   chan auditEntry
	mu      sync.Mutex
	failure error
	done    chan struct{}
	once    sync.Once
}

func OpenAudit(path string) (*Logger, error) {
	if st, e := os.Lstat(path); e == nil {
		if !st.Mode().IsRegular() || (runtime.GOOS != "windows" && st.Mode().Perm()&0077 != 0) {
			return nil, errors.New("existing audit file must be regular and private (0600)")
		}
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if e != nil {
		return nil, e
	}
	a := &Logger{file: f, queue: make(chan auditEntry, 32), done: make(chan struct{})}
	go func() {
		for {
			var r auditEntry
			select {
			case <-a.done:
				return
			case r = <-a.queue:
			}
			_, e := f.Write(r.b)
			a.mu.Lock()
			if e != nil {
				a.failure = e
			}
			a.mu.Unlock()
			r.ack <- e
		}
	}()
	return a, nil
}
func redact(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			switch k {
			case "password", "key_passphrase", "token", "private_key":
				t[k] = "[REDACTED]"
			default:
				t[k] = redact(x)
			}
		}
	case []any:
		for i, x := range t {
			t[i] = redact(x)
		}
	}
	return v
}
func (a *Logger) Record(v any) error {
	a.mu.Lock()
	e := a.failure
	a.mu.Unlock()
	if e != nil {
		return e
	}
	b, e := json.Marshal(map[string]any{"time": time.Now().UTC().Format(time.RFC3339Nano), "input": redact(v)})
	if e != nil {
		return e
	}
	r := auditEntry{b: append(b, '\n'), ack: make(chan error, 1)}
	select {
	case <-a.done:
		return errors.New("audit closed")
	case a.queue <- r:
	default:
		return errors.New("audit queue full; input was not executed")
	}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case e := <-r.ack:
		return e
	case <-timer.C:
		return errors.New("audit write deadline; input was not executed")
	}
}
func (a *Logger) Close() { a.once.Do(func() { close(a.done); go func() { _ = a.file.Close() }() }) }
