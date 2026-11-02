package scenarios

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKeep(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "n2")
	r := &Runner{OutDir: dir}
	if err := r.keep("dba", "S01", "hello"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "S01-dba.txt"))
	if err != nil || string(b) != "hello" {
		t.Fatalf("got %q, %v", b, err)
	}
	if err := (&Runner{}).keep("dba", "S01", "x"); err != nil {
		t.Fatalf("no OutDir must be a no-op: %v", err)
	}
}
