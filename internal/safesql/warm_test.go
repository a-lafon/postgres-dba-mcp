package safesql

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const warmChildEnv = "SAFESQL_WARM_CHILD"

// runFresh re-executes the test binary for one child test, so that no earlier
// test has warmed the parser.
func runFresh(t *testing.T, child string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^"+child+"$", "-test.v")
	cmd.Env = append(os.Environ(), warmChildEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child %s failed: %v\n%s", child, err, out)
	}
	if strings.Contains(string(out), "--- SKIP") || !strings.Contains(string(out), "--- PASS: "+child) {
		t.Fatalf("child %s did not run:\n%s", child, out)
	}
}

func TestWarmReturnsAtOnce(t *testing.T) { runFresh(t, "TestWarmReturnsAtOnceChild") }

func TestWarmReturnsAtOnceChild(t *testing.T) {
	if os.Getenv(warmChildEnv) == "" {
		t.Skip("runs in a fresh process, see TestWarmReturnsAtOnce")
	}
	start := time.Now()
	Warm()
	// A synchronous warm-up compiles the WASM module: about 0.7s, 9s under -race. 100ms leaves room for a loaded runner.
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Errorf("Warm took %v, want under 100ms", d)
	}
	if _, err := Check("SELECT 1", defaultPolicy(t)); err != nil {
		t.Fatal(err)
	}
}

func TestWarmConcurrent(t *testing.T) { runFresh(t, "TestWarmConcurrentChild") }

func TestWarmConcurrentChild(t *testing.T) {
	if os.Getenv(warmChildEnv) == "" {
		t.Skip("runs in a fresh process, see TestWarmConcurrent")
	}
	p := defaultPolicy(t)
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			Warm()
			_, err := Check("SELECT 1", p)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
}

func TestClassifyDuringWarm(t *testing.T) { runFresh(t, "TestClassifyDuringWarmChild") }

func TestClassifyDuringWarmChild(t *testing.T) {
	if os.Getenv(warmChildEnv) == "" {
		t.Skip("runs in a fresh process, see TestClassifyDuringWarm")
	}
	Warm()
	if n, k, err := Classify("SELECT 1"); err != nil || n != 1 || k != Select {
		t.Fatalf("got (%d, %v, %v)", n, k, err)
	}
}

func TestWarmReleasesCompileGarbage(t *testing.T) { runFresh(t, "TestWarmReleasesCompileGarbageChild") }

func TestWarmReleasesCompileGarbageChild(t *testing.T) {
	if os.Getenv(warmChildEnv) == "" {
		t.Skip("runs in a fresh process, see TestWarmReleasesCompileGarbage")
	}
	// The first parse, not Warm, triggers the compile; it must release too.
	if _, err := Check("SELECT 1", defaultPolicy(t)); err != nil {
		t.Fatal(err)
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	// Without the release the compile leaves ~250 MB retained; with it, the
	// live heap (~8 MB) plus runtime overhead stays far below the bound.
	const bound = 64 << 20
	if held := m.HeapSys - m.HeapReleased; held > bound {
		t.Errorf("heap held after warm-up = %d MB, want under %d MB", held>>20, bound>>20)
	}
}

func BenchmarkParse(b *testing.B) {
	cases := loadCorpus(b, "legit.sql", map[string]bool{"legit": true})
	Warm()
	if _, err := parse("SELECT 1"); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := range b.N {
		if _, err := parse(cases[i%len(cases)].SQL); err != nil {
			b.Fatal(err)
		}
	}
}
