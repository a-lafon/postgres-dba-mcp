// Package load builds the bench database: it runs the SQL files through psql in the pg container, fetches and
// verifies Pagila, and replays the workload.
package load

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// File is one line of pagila.lock.
type File struct {
	Name   string
	SHA256 string
	URL    string
}

// ParseLock reads pagila.lock: `<name> <sha256> <url>` per line, `#` comments and blank lines ignored.
func ParseLock(r io.Reader) ([]File, error) {
	var files []File
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 3 {
			return nil, fmt.Errorf("pagila.lock line %q: want `<name> <sha256> <url>`", line)
		}
		files = append(files, File{Name: f[0], SHA256: f[1], URL: f[2]})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read pagila.lock: %w", err)
	}
	if len(files) == 0 {
		return nil, errors.New("pagila.lock lists no file")
	}
	return files, nil
}

// ChecksumError is returned by Fetch when a downloaded file does not match the lock.
type ChecksumError struct {
	Name     string
	Expected string
	Actual   string
}

func (e *ChecksumError) Error() string {
	return fmt.Sprintf("pagila: %s checksum mismatch: expected sha256 %s, got %s", e.Name, e.Expected, e.Actual)
}

// Fetch downloads every file of the lock and verifies its sha256, returning the contents by name. It returns a
// *ChecksumError, and no content, when any file does not match.
func Fetch(ctx context.Context, client *http.Client, files []File) (map[string][]byte, error) {
	out := make(map[string][]byte, len(files))
	for _, f := range files {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.URL, nil)
		if err != nil {
			return nil, fmt.Errorf("fetch %s: %w", f.Name, err)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetch %s: %w", f.Name, err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("fetch %s: %w", f.Name, err)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("fetch %s: %s", f.Name, resp.Status)
		}
		sum := sha256.Sum256(body)
		if got := hex.EncodeToString(sum[:]); got != f.SHA256 {
			return nil, &ChecksumError{Name: f.Name, Expected: f.SHA256, Actual: got}
		}
		out[f.Name] = body
	}
	return out, nil
}

// Options configure Run.
type Options struct {
	BenchDir string // the bench folder: compose.yml and data/ live there
	Scale    float64
	Pagila   bool
	Log      io.Writer
	Client   *http.Client
	Workload time.Duration // pgbench duration, 120 s by default
}

// Run loads the bench database: roles, base data, Pagila (unless skipped), traps, documents, grants, ANALYZE,
// then resets pg_stat_statements and replays the workload.
func Run(ctx context.Context, o Options) error {
	if o.Workload == 0 {
		o.Workload = 120 * time.Second
	}
	if o.Client == nil {
		o.Client = http.DefaultClient
	}
	var pagila map[string][]byte
	if o.Pagila {
		lock, err := os.Open(filepath.Join(o.BenchDir, "data", "pagila.lock"))
		if err != nil {
			return fmt.Errorf("open pagila.lock: %w", err)
		}
		files, err := ParseLock(lock)
		_ = lock.Close()
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(o.Log, "load: fetching Pagila")
		if pagila, err = Fetch(ctx, o.Client, files); err != nil {
			return err
		}
	}
	psql := func(name string, sql []byte) error {
		_, _ = fmt.Fprintf(o.Log, "load: %s\n", name)
		return o.compose(ctx, bytes.NewReader(sql), "exec", "-T", "pg", "psql", "-q", "-U", "postgres", "-d", "bench",
			"-v", "ON_ERROR_STOP=1", "-v", "scale="+strconv.FormatFloat(o.Scale, 'f', -1, 64), "-f", "-")
	}
	steps := []string{"00_roles.sql", "10_base.sql", "pagila", "20_traps.sql", "30_docs.sql", "99_grants.sql"}
	for _, name := range steps {
		if name == "pagila" {
			if o.Pagila {
				// Pagila defines its own public objects; its data file feeds COPY ... FROM stdin blocks.
				for _, f := range []string{"pagila-schema.sql", "pagila-data.sql"} {
					if err := psql(f, pagila[f]); err != nil {
						return fmt.Errorf("load %s: %w", f, err)
					}
				}
			}
			continue
		}
		sql, err := os.ReadFile(filepath.Join(o.BenchDir, "data", name))
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		if err := psql(name, sql); err != nil {
			return fmt.Errorf("load %s: %w", name, err)
		}
	}
	meta := fmt.Sprintf("COMMENT ON DATABASE bench IS '%s'", MetaText(o.Scale, o.Pagila))
	for _, q := range []string{"ANALYZE", "SELECT pg_stat_statements_reset()", meta} {
		if err := psql(q, []byte(q+";")); err != nil {
			return err
		}
	}
	workload, err := os.ReadFile(filepath.Join(o.BenchDir, "data", "workload.pgbench"))
	if err != nil {
		return fmt.Errorf("read workload: %w", err)
	}
	_, _ = fmt.Fprintf(o.Log, "load: workload for %s\n", o.Workload)
	err = o.compose(ctx, bytes.NewReader(workload), "exec", "-T", "pg", "pgbench", "-n", "-f", "/dev/stdin",
		"-T", strconv.Itoa(int(o.Workload.Seconds())), "-c", "4", "-U", "postgres", "bench")
	if err != nil {
		return fmt.Errorf("run workload: %w", err)
	}
	return nil
}

func (o Options) compose(ctx context.Context, stdin io.Reader, args ...string) error {
	cmd := exec.CommandContext(ctx, "docker", append([]string{"compose", "-f", filepath.Join(o.BenchDir, "compose.yml")}, args...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, io.Discard, o.Log
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker compose %s: %w", strings.Join(args[:min(len(args), 4)], " "), err)
	}
	return nil
}

// MetaText is the database comment that records how the bench database was loaded.
func MetaText(scale float64, pagila bool) string {
	return fmt.Sprintf("scale=%s;pagila=%t", strconv.FormatFloat(scale, 'f', -1, 64), pagila)
}

// ParseMeta reads a MetaText comment.
func ParseMeta(s string) (scale float64, pagila bool, err error) {
	for _, kv := range strings.Split(s, ";") {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case "scale":
			if scale, err = strconv.ParseFloat(v, 64); err != nil {
				return 0, false, fmt.Errorf("database comment %q: %w", s, err)
			}
		case "pagila":
			pagila = v == "true"
		}
	}
	if scale == 0 {
		return 0, false, fmt.Errorf("database comment %q does not say how the bench database was loaded: run load", s)
	}
	return scale, pagila, nil
}
