package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCommands(t *testing.T) {
	for _, args := range [][]string{nil, {"help"}, {"mcp-skill"}, {"version"}} {
		var out bytes.Buffer
		if e := Run(context.Background(), args, strings.NewReader(""), &out, &out); e != nil || out.Len() == 0 {
			t.Fatal(args, e)
		}
	}
	dir := filepath.Join(t.TempDir(), "skill")
	var out bytes.Buffer
	if e := Run(context.Background(), []string{"install-skill", "--dir", dir}, nil, &out, &out); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if e != nil || string(b) != Skill {
		t.Fatal("skill mismatch", e)
	}
	if e = Run(context.Background(), []string{"install-skill", "--dir", dir}, nil, &out, &out); e == nil {
		t.Fatal("overwrote existing skill")
	}
}
func TestAuditRedaction(t *testing.T) {
	p := filepath.Join(t.TempDir(), "audit.jsonl")
	a, e := OpenAudit(p)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	if e = a.Record(map[string]any{"arguments": map[string]any{"password": "password-secret", "key_passphrase": "key-secret", "command": "show version"}}); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(b), "password-secret") || strings.Contains(string(b), "key-secret") || !strings.Contains(string(b), "show version") {
		t.Fatal(string(b))
	}
	var v any
	if e = json.Unmarshal(b, &v); e != nil {
		t.Fatal(e)
	}
	st, _ := os.Stat(p)
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0600 {
		t.Fatal(st.Mode())
	}
}
