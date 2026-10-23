//go:build integration

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/a-lafon/postgres-dba-mcp/internal/config"
	"github.com/a-lafon/postgres-dba-mcp/internal/testpg"
)

var binary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "pgdba-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binary = filepath.Join(dir, "postgres-dba-mcp")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build: %v\n%s", err, out)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// syncBuf is a bytes.Buffer safe for the exec package's copying goroutine.
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// proc is the built binary speaking raw JSON-RPC over its stdio.
type proc struct {
	t      *testing.T
	cmd    *exec.Cmd
	in     io.WriteCloser
	lines  chan string
	stderr *syncBuf
	stdout []string // every line read, in order
	nextID int
	done   chan struct{} // closed when the process has exited
	exit   error
}

func start(t *testing.T, args ...string) *proc {
	t.Helper()
	p := &proc{t: t, cmd: exec.Command(binary, args...), stderr: &syncBuf{}, lines: make(chan string, 64), done: make(chan struct{})}
	p.cmd.Stderr = p.stderr
	var err error
	if p.in, err = p.cmd.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	out, err := p.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			p.lines <- sc.Text()
		}
		close(p.lines)
	}()
	go func() { p.exit = p.cmd.Wait(); close(p.done) }()
	t.Cleanup(func() {
		_ = p.cmd.Process.Kill()
		select {
		case <-p.done:
		case <-time.After(2 * time.Second):
		}
	})
	return p
}

func (p *proc) send(msg map[string]any) {
	p.t.Helper()
	msg["jsonrpc"] = "2.0"
	b, _ := json.Marshal(msg)
	if _, err := fmt.Fprintf(p.in, "%s\n", b); err != nil {
		p.t.Fatal(err)
	}
}

// request sends a request and returns the response with the same id.
func (p *proc) request(method string, params any) map[string]any {
	p.t.Helper()
	p.nextID++
	id := p.nextID
	p.send(map[string]any{"id": id, "method": method, "params": params})
	for {
		select {
		case line, ok := <-p.lines:
			if !ok {
				p.t.Fatalf("stdout closed while waiting for %s; stderr: %s", method, p.stderr.String())
			}
			p.stdout = append(p.stdout, line)
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				p.t.Fatalf("stdout line is not JSON: %q", line)
			}
			if m["id"] == float64(id) {
				return m
			}
		case <-time.After(15 * time.Second):
			p.t.Fatalf("no response to %s; stderr: %s", method, p.stderr.String())
		}
	}
}

func (p *proc) initialize() map[string]any {
	p.t.Helper()
	res := p.request("initialize", map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "test", "version": "1"},
	})
	p.send(map[string]any{"method": "notifications/initialized"})
	return res
}

// callTool returns isError and the text of the single content block.
func (p *proc) callTool(name string, args map[string]any) (bool, string) {
	p.t.Helper()
	params := map[string]any{"name": name}
	if args != nil {
		params["arguments"] = args
	}
	res := p.request("tools/call", params)
	result, ok := res["result"].(map[string]any)
	if !ok {
		p.t.Fatalf("tools/call %s: no result in %v", name, res)
	}
	content := result["content"].([]any)
	if len(content) != 1 {
		p.t.Fatalf("content has %d items, want 1", len(content))
	}
	isErr, _ := result["isError"].(bool)
	return isErr, content[0].(map[string]any)["text"].(string)
}

func (p *proc) wait(timeout time.Duration) (int, bool) {
	select {
	case <-p.done:
		if p.exit == nil {
			return 0, true
		}
		if ee, ok := p.exit.(*exec.ExitError); ok {
			return ee.ExitCode(), true
		}
		return -1, true
	case <-time.After(timeout):
		return 0, false
	}
}

func TestDatabaseDownAtStartup(t *testing.T) {
	p := start(t, "postgres://pgdba_ro:pgdba@127.0.0.1:1/pgdba?sslmode=disable")
	if res := p.initialize(); res["error"] != nil {
		t.Fatalf("initialize: %v", res)
	}
	if res := p.request("tools/list", map[string]any{}); res["error"] != nil {
		t.Fatalf("tools/list: %v", res)
	}
	isErr, text := p.callTool("list_schemas", nil)
	if !isErr || !strings.HasPrefix(text, "ERROR: cannot connect to the database:") {
		t.Errorf("call = %v %q", isErr, text)
	}
}

func TestPasswordNeverLogged(t *testing.T) {
	const pw = "wrongpw-xyz-123"
	base := testpg.RO(t)
	pc := config.Config{DSN: base}
	target := strings.TrimPrefix(pc.Redacted(), "pgdba_ro@") // host:port/db
	dsn := fmt.Sprintf("postgres://pgdba_ro:%s@%s?sslmode=disable", pw, target)
	p := start(t, dsn)
	p.initialize()
	isErr, text := p.callTool("list_schemas", nil)
	if !isErr || !strings.HasPrefix(text, "ERROR: cannot connect to the database:") {
		t.Errorf("wrong password call = %v %q", isErr, text)
	}
	if strings.Contains(text, pw) || strings.Contains(p.stderr.String(), pw) {
		t.Errorf("password found in the tool text or stderr: %q / %q", text, p.stderr.String())
	}
	if !strings.Contains(p.stderr.String(), "target pgdba_ro@"+target+",") {
		t.Errorf("stderr = %q", p.stderr.String())
	}
}

func TestStartupLine(t *testing.T) {
	dsn := testpg.RO(t)
	p := start(t, dsn)
	p.initialize()
	target := (&config.Config{DSN: dsn}).Redacted()
	want := "postgres-dba-mcp dev: target " + target + ", access mode restricted, transport stdio\n"
	if !strings.Contains(p.stderr.String(), want) {
		t.Errorf("stderr = %q, want it to contain %q", p.stderr.String(), want)
	}
	if !regexp.MustCompile(`^pgdba_ro@localhost:\d+/pgdba$`).MatchString(target) {
		t.Errorf("target = %q", target)
	}
}

func TestUnrestrictedWarningAndStdoutProtocolOnly(t *testing.T) {
	p := start(t, "--access-mode=unrestricted", testpg.RO(t))
	p.initialize()
	p.request("tools/list", map[string]any{})
	p.callTool("list_schemas", nil)
	const warning = "WARNING: unrestricted mode: tools can modify the database; use a read-only role or --access-mode=restricted outside development"
	if !strings.Contains(p.stderr.String(), warning) {
		t.Errorf("stderr = %q", p.stderr.String())
	}
	if len(p.stdout) == 0 {
		t.Fatal("no stdout line read")
	}
	for _, l := range p.stdout {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil || m["jsonrpc"] != "2.0" {
			t.Errorf("stdout line is not a JSON-RPC message: %q", l)
		}
	}
}

func TestNoConnectionBeforeFirstCall(t *testing.T) {
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, testpg.Admin(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close(ctx) }()
	count := func() int {
		var n int
		if err := admin.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE application_name = 'postgres-dba-mcp'").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	p := start(t, testpg.RO(t))
	p.initialize()
	p.request("tools/list", map[string]any{})
	if n := count(); n != 0 {
		t.Errorf("sessions after tools/list = %d, want 0", n)
	}
	p.callTool("list_schemas", nil)
	if n := count(); n == 0 {
		t.Error("no session after the first call")
	}
}

func TestSIGTERMWhileIdle(t *testing.T) {
	p := start(t, testpg.RO(t))
	p.initialize()
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code, ok := p.wait(2 * time.Second); !ok || code != 0 {
		t.Errorf("exit code = %d, exited = %v, want 0 within 2s", code, ok)
	}
}

func TestStdinClosed(t *testing.T) {
	p := start(t, testpg.RO(t))
	p.initialize()
	_ = p.in.Close()
	if code, ok := p.wait(2 * time.Second); !ok || code != 0 {
		t.Errorf("exit code = %d, exited = %v, want 0 within 2s; stderr: %s", code, ok, p.stderr.String())
	}
}

func TestUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no URI", nil, "error: no database URI: pass it as an argument or set DATABASE_URI\n"},
		{"bad group", []string{"--tools=schema,foo", "postgres://u@h/db"}, "error: --tools: unknown group \"foo\""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(binary, tt.args...)
			cmd.Env = append(os.Environ(), "DATABASE_URI=")
			var stderr syncBuf
			cmd.Stderr = &stderr
			err := cmd.Run()
			ee, ok := err.(*exec.ExitError)
			if !ok || ee.ExitCode() != 2 {
				t.Errorf("err = %v, want exit status 2", err)
			}
			if !strings.HasPrefix(stderr.String(), tt.want) {
				t.Errorf("stderr = %q, want prefix %q", stderr.String(), tt.want)
			}
		})
	}
}

func TestVersion(t *testing.T) {
	cmd := exec.Command(binary, "--version")
	cmd.Env = append(os.Environ(), "DATABASE_URI=")
	out, err := cmd.Output()
	if err != nil || string(out) != "postgres-dba-mcp dev\n" {
		t.Errorf("output = %q, err = %v", out, err)
	}
}

func TestListSchemasEndToEnd(t *testing.T) {
	p := start(t, testpg.RO(t))
	res := p.initialize()
	info := res["result"].(map[string]any)["serverInfo"].(map[string]any)
	if info["name"] != "postgres-dba-mcp" {
		t.Errorf("serverInfo = %v", info)
	}
	isErr, text := p.callTool("list_schemas", nil)
	if isErr {
		t.Fatalf("list_schemas failed: %s", text)
	}
	if !strings.HasPrefix(text, "schema_name,owner,type,tables\n") || !strings.Contains(text, "\npublic,") {
		t.Errorf("text = %q", text)
	}
	if !regexp.MustCompile(`\n-- \d+ rows, \d+ ms\n$`).MatchString(text) {
		t.Errorf("footer missing in %q", text)
	}
	isErr, text = p.callTool("list_schemas", map[string]any{"foo": 1})
	if !isErr || text != `ERROR: invalid arguments: unknown field "foo"` {
		t.Errorf("unknown argument = %v %q", isErr, text)
	}
}

// TestStartupLatency logs how long the binary takes from exec to the answer to initialize.
func TestStartupLatency(t *testing.T) {
	best := time.Hour
	for i := 0; i < 10; i++ {
		begin := time.Now()
		p := start(t, "postgres://pgdba_ro:pgdba@127.0.0.1:1/pgdba?sslmode=disable")
		p.initialize()
		if d := time.Since(begin); d < best {
			best = d
		}
		_ = p.cmd.Process.Kill()
	}
	t.Logf("fastest of 10 runs, exec to initialize response: %v", best)
}
