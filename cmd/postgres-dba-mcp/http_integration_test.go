//go:build integration

package main

import (
	"errors"
	"net"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/a-lafon/postgres-dba-mcp/internal/testpg"
)

// freePort returns a loopback port that was free a moment ago.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	return port
}

func TestHTTPSIGTERMWhileIdle(t *testing.T) {
	port := freePort(t)
	cmd := exec.Command(binary, "--transport=streamable-http", "--port="+port, testpg.RO(t))
	stderr := &syncBuf{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(stderr.String(), "listening on http://127.0.0.1:"+port+"/mcp") {
		if time.Now().After(deadline) {
			t.Fatalf("server not listening; stderr: %s", stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("exit = %v, want status 0; stderr: %s", err, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no exit within 5s of SIGTERM")
	}
}

func TestHTTPFlagErrorsExit2(t *testing.T) {
	out, err := exec.Command(binary, "--transport=streamable-http", "--host=0.0.0.0", "postgres://u@h/db").CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 2 {
		t.Fatalf("err = %v, want exit status 2", err)
	}
	if want := "error: --host 0.0.0.0: binding a non-loopback address requires --allow-remote\n"; string(out) != want {
		t.Errorf("output = %q, want %q", out, want)
	}
}
