// Package ops measures what running the servers costs: cold start, resident memory, size and concurrency.
package ops

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/mcpc"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/scenarios"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/servers"
)

// Measure counts.
const (
	ColdStarts     = 20
	RSSCalls       = 100
	MemSamples     = 3 // containers per server, the memory measure being their median
	Concurrent     = 50
	crystalLabel   = "includes docker run"
	rssResponseCap = 512 << 20
	// ImageLabel names what the postgres-dba-mcp memory target reads.
	ImageLabel = "linux image, docker stats"
)

// Options configure Run.
type Options struct {
	DBA     servers.Spec // the native binary: cold start, informational memory
	Image   servers.Spec // the linux image: the memory measure of the target
	Crystal servers.Spec
	Bin     string // path of the postgres-dba-mcp binary, for its size
	DSN     string // connection string of the bench database, for the HTTP server
	Pagila  bool
	Log     io.Writer
}

// Run takes every ops measure. The RSS and concurrency measures need Pagila (they call S02 and S01).
func Run(ctx context.Context, o Options) (results.Ops, error) {
	out := results.Ops{
		ColdStartMS: map[string]results.Measure{}, RSSMB: map[string]results.Measure{}, SizeMB: map[string]results.Measure{},
	}
	for _, spec := range []servers.Spec{o.DBA, o.Crystal} {
		_, _ = fmt.Fprintf(o.Log, "ops cold start %s\n", spec.Name)
		ms, err := coldStart(ctx, spec)
		if err != nil {
			return out, err
		}
		m := results.Measure{Value: ms}
		if spec.Name == results.Crystal {
			m.Note = crystalLabel
		}
		out.ColdStartMS[spec.Name] = m
	}
	fi, err := os.Stat(o.Bin)
	if err != nil {
		return out, fmt.Errorf("binary size: %w", err)
	}
	out.SizeMB[results.DBA] = results.Measure{Value: float64(fi.Size()) / (1 << 20), Note: "binary"}
	img, err := imageSize(ctx, servers.CrystalImage)
	if err != nil {
		return out, err
	}
	out.SizeMB[results.Crystal] = results.Measure{Value: img, Note: "image"}

	if !o.Pagila {
		skip := results.Measure{Skipped: true, Note: "needs Pagila"}
		out.RSSMB[results.DBA], out.RSSMB[results.Crystal], out.NativeRSSMB, out.Concurrency.Skipped = skip, skip, skip, true
		return out, nil
	}
	// The target reads the image, as shipped to containers and measured like crystaldba; the native binary is informational.
	for _, m := range []struct {
		spec servers.Spec
		note string
	}{{o.Image, ImageLabel}, {o.Crystal, ""}} {
		_, _ = fmt.Fprintf(o.Log, "ops rss %s\n", m.spec.Name)
		var samples []float64
		for i := 0; i < MemSamples; i++ {
			mb, _, err := rss(ctx, m.spec)
			if err != nil {
				return out, err
			}
			samples = append(samples, mb)
		}
		out.RSSMB[m.spec.Name] = medianMeasure(samples, m.note)
	}
	_, _ = fmt.Fprintln(o.Log, "ops rss native")
	mb, what, err := rss(ctx, o.DBA)
	if err != nil {
		return out, err
	}
	out.NativeRSSMB = results.Measure{Value: mb, Note: what}
	_, _ = fmt.Fprintln(o.Log, "ops concurrency")
	out.Concurrency.Calls = Concurrent
	out.Concurrency.Errors, err = concurrency(ctx, o)
	return out, err
}

// coldStart returns the median, in ms, of ColdStarts times from process start to the initialize response.
func coldStart(ctx context.Context, spec servers.Spec) (float64, error) {
	var ms []float64
	for i := 0; i < ColdStarts; i++ {
		start := time.Now()
		srv, err := spec.Start(ctx)
		if err != nil {
			return 0, err
		}
		ms = append(ms, float64(time.Since(start).Microseconds())/1000)
		srv.Close()
	}
	return scenarios.Median(ms), nil
}

// medianMeasure returns the measure whose value is the median of the samples, which it keeps.
func medianMeasure(samples []float64, note string) results.Measure {
	return results.Measure{Value: scenarios.Median(samples), Note: note, Samples: samples}
}

var reMem = regexp.MustCompile(`^([\d.]+)\s*([KMG]i?B)`)

// rss returns the memory, in MB, of the server after RSSCalls calls of S02, and a label of what was read.
// A native server is read as its physical footprint on macOS, where ps still counts the pages Go freed with
// MADV_FREE, and as VmRSS on Linux. crystaldba comes from docker stats and carries no label.
func rss(ctx context.Context, spec servers.Spec) (float64, string, error) {
	// crystaldba answers S02 with every row of rental, more than the 16 MiB cap of the other measures at this size.
	srv, err := spec.StartLimit(ctx, rssResponseCap)
	if err != nil {
		return 0, "", err
	}
	defer srv.Close()
	for i := 0; i < RSSCalls; i++ {
		if _, err := srv.Call(ctx, "execute_sql", map[string]any{"sql": "SELECT * FROM rental"}); err != nil {
			return 0, "", fmt.Errorf("rss call %d on %s: %w", i+1, spec.Name, err)
		}
	}
	if spec.Container != "" {
		out, err := exec.CommandContext(ctx, "docker", "stats", "--no-stream", "--format", "{{.MemUsage}}", spec.Container).Output()
		if err != nil {
			return 0, "", fmt.Errorf("docker stats: %w", err)
		}
		mb, err := parseMem(strings.TrimSpace(string(out)))
		return mb, "", err
	}
	pid := strconv.Itoa(srv.Pid())
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.CommandContext(ctx, "footprint", "-p", pid, "--noCategories").Output()
		if err != nil {
			return 0, "", fmt.Errorf("footprint: %w", err)
		}
		mb, err := parseFootprint(string(out))
		return mb, "phys_footprint", err
	case "linux":
		b, err := os.ReadFile("/proc/" + pid + "/status")
		if err != nil {
			return 0, "", fmt.Errorf("read process status: %w", err)
		}
		mb, err := parseVmRSS(string(b))
		return mb, "RSS", err
	}
	return 0, "", fmt.Errorf("no memory reader for %s", runtime.GOOS)
}

var (
	reFootprint = regexp.MustCompile(`(?m)^\s*phys_footprint:\s*([\d.]+)\s*([KMG]?B)\b`)
	reVmRSS     = regexp.MustCompile(`(?m)^VmRSS:\s*(\d+)\s*kB`)
)

// parseFootprint reads the phys_footprint line of `footprint -p <pid>`, such as "phys_footprint: 36 MB", in MB.
func parseFootprint(s string) (float64, error) {
	m := reFootprint.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("no phys_footprint in footprint output %q", s)
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, fmt.Errorf("unreadable phys_footprint %q: %w", m[1], err)
	}
	switch m[2] {
	case "B":
		return v / (1 << 20), nil
	case "KB":
		return v / 1024, nil
	case "GB":
		return v * 1024, nil
	}
	return v, nil
}

// parseVmRSS reads the VmRSS line of /proc/<pid>/status, in MB.
func parseVmRSS(s string) (float64, error) {
	m := reVmRSS.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("no VmRSS in process status %q", s)
	}
	kb, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, fmt.Errorf("unreadable VmRSS %q: %w", m[1], err)
	}
	return kb / 1024, nil
}

// parseMem reads the usage part of a `docker stats` MemUsage cell, such as "123.4MiB / 7.6GiB", in MB.
func parseMem(s string) (float64, error) {
	m := reMem.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("unreadable memory usage %q", s)
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, fmt.Errorf("unreadable memory usage %q: %w", s, err)
	}
	switch strings.ToUpper(m[2][:1]) {
	case "K":
		return v / 1024, nil
	case "G":
		return v * 1024, nil
	}
	return v, nil
}

func imageSize(ctx context.Context, image string) (float64, error) {
	out, err := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{.Size}}", image).Output()
	if err != nil {
		return 0, fmt.Errorf("inspect image %s: %w", image, err)
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0, fmt.Errorf("parse image size %q: %w", out, err)
	}
	return n / (1 << 20), nil
}

// concurrency starts postgres-dba-mcp over streamable HTTP and sends Concurrent S01 calls at once, one session each.
// It returns the number of calls that failed.
func concurrency(ctx context.Context, o Options) (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("find a free port: %w", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	spec := servers.DBA(o.Bin, o.DSN, "--transport=streamable-http", "--port="+strconv.Itoa(port))
	cmd := exec.CommandContext(ctx, spec.Argv[0], spec.Argv[1:]...)
	cmd.Env = append(os.Environ(), spec.Env...)
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start HTTP server: %w", err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/mcp", port)
	if err := waitFor(ctx, endpoint); err != nil {
		return 0, err
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	failed := 0
	for i := 0; i < Concurrent; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok := false
			if c, err := mcpc.ConnectHTTP(ctx, endpoint); err == nil {
				res, err := c.Call(ctx, "execute_sql", map[string]any{"sql": "SELECT * FROM film LIMIT 10"})
				ok = err == nil && !res.IsError
				c.Close()
			}
			if !ok {
				mu.Lock()
				failed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return failed, nil
}

// waitFor polls until the HTTP endpoint accepts an MCP session.
func waitFor(ctx context.Context, endpoint string) error {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := mcpc.ConnectHTTP(ctx, endpoint); err == nil {
			c.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("HTTP server at %s did not come up", endpoint)
}
