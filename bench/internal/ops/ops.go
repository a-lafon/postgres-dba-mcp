// Package ops measures what running each server costs: image size, cold start and memory. They are context for the
// report, never a result (PROTOCOL.md §1). Every server runs from its registry image on the bench network,
// postgres-dba-mcp included, so the servers are compared image against image; the published figures come from a
// GitHub Actions x86-64 runner (§9), where no image runs emulated.
package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/scenarios"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/servers"
)

// Measure counts.
const (
	ColdStarts = 20
	MemCalls   = 100
	MemSamples = 3 // containers per server, the memory measure being their median
	// memSQL is S02, which returns every row of Pagila's rental table.
	memSQL = "SELECT * FROM rental"
	// memResponseCap lets every server answer S02 in full: crystaldba's answer exceeds the 16 MiB cap of the other levels.
	memResponseCap = 512 << 20
	coldSQL        = "SELECT 1"
	// Notes of the measures, kept in ops.json.
	ColdNote = "docker run to the answer of the first SQL call"
	MemNote  = "docker stats"
	SizeNote = "docker image inspect"
)

// Options configure Run.
type Options struct {
	Servers []servers.Pinned // their images must be present (Pinned.Prepare)
	URI     string           // the database as a container on the bench network sees it
	Pagila  bool
	Log     io.Writer
}

// Run takes every ops measure of every server. The memory measure needs Pagila (S02 reads rental).
func Run(ctx context.Context, o Options) (results.Ops, error) {
	out := results.Ops{
		ColdStartMS: map[string]results.Measure{}, RSSMB: map[string]results.Measure{}, SizeMB: map[string]results.Measure{},
	}
	out.Concurrency.Skipped = true // protocol v2 measures no concurrency
	var err error
	if out.Host, err = host(ctx); err != nil {
		return out, err
	}
	for _, p := range o.Servers {
		size, err := imageSize(ctx, p.Image)
		if err != nil {
			return out, err
		}
		out.SizeMB[p.Name] = results.Measure{Value: size, Note: SizeNote}

		_, _ = fmt.Fprintf(o.Log, "ops cold start %s\n", p.Name)
		ms, err := coldStart(ctx, p, o.URI)
		if err != nil {
			return out, err
		}
		out.ColdStartMS[p.Name] = medianMeasure(ms, ColdNote)

		if !o.Pagila {
			out.RSSMB[p.Name] = results.Measure{Skipped: true, Note: "needs Pagila"}
			continue
		}
		_, _ = fmt.Fprintf(o.Log, "ops memory %s\n", p.Name)
		var samples []float64
		for i := 0; i < MemSamples; i++ {
			mb, err := memory(ctx, p, o.URI)
			if err != nil {
				return out, err
			}
			samples = append(samples, mb)
		}
		out.RSSMB[p.Name] = medianMeasure(samples, MemNote)
	}
	return out, nil
}

// coldStart returns ColdStarts times, in ms, from `docker run` to the answer of a first SQL call: the initialize
// handshake, then SELECT 1 through the server's SQL tool, which also opens its first database connection.
func coldStart(ctx context.Context, p servers.Pinned, uri string) ([]float64, error) {
	var ms []float64
	for i := 0; i < ColdStarts; i++ {
		start := time.Now()
		srv, err := p.Spec(uri).Start(ctx)
		if err != nil {
			return nil, err
		}
		res, err := srv.Call(ctx, p.SQLTool, map[string]any{p.SQLArg: coldSQL})
		d := time.Since(start)
		srv.Close()
		if err := callErr(p.Name, "cold start", res.IsError, res.Text, err); err != nil {
			return nil, err
		}
		ms = append(ms, float64(d.Microseconds())/1000)
	}
	return ms, nil
}

// memory returns the memory, in MB, that docker stats reads for one container of the server after MemCalls calls of
// S02.
func memory(ctx context.Context, p servers.Pinned, uri string) (float64, error) {
	spec := p.Spec(uri)
	srv, err := spec.StartLimit(ctx, memResponseCap)
	if err != nil {
		return 0, err
	}
	defer srv.Close()
	for i := 0; i < MemCalls; i++ {
		res, err := srv.Call(ctx, p.SQLTool, map[string]any{p.SQLArg: memSQL})
		if err := callErr(p.Name, fmt.Sprintf("memory call %d", i+1), res.IsError, res.Text, err); err != nil {
			return 0, err
		}
	}
	out, err := exec.CommandContext(ctx, "docker", "stats", "--no-stream", "--format", "{{.MemUsage}}", spec.Container).Output()
	if err != nil {
		return 0, fmt.Errorf("docker stats: %w", err)
	}
	return parseMem(strings.TrimSpace(string(out)))
}

// callErr fails a measure whose call failed: a server that answered with an error did not do the work measured.
func callErr(server, what string, isError bool, text string, err error) error {
	if err != nil {
		return fmt.Errorf("%s on %s: %w", what, server, err)
	}
	if isError {
		return fmt.Errorf("%s on %s: the server answered with an error: %.200s", what, server, text)
	}
	return nil
}

// medianMeasure returns the measure whose value is the median of the samples, which it keeps.
func medianMeasure(samples []float64, note string) results.Measure {
	return results.Measure{Value: scenarios.Median(samples), Note: note, Samples: samples}
}

var reMem = regexp.MustCompile(`^([\d.]+)\s*([KMG]i?B)`)

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

// host describes the machine the containers ran on, as the Docker daemon reports it.
func host(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{json .}}").Output()
	if err != nil {
		return "", fmt.Errorf("docker info: %w", err)
	}
	return parseHost(out)
}

// parseHost reads the output of `docker info --format '{{json .}}'` into "<os>, <arch>, <n> CPUs, <mem> GiB".
func parseHost(b []byte) (string, error) {
	var info struct {
		OperatingSystem, Architecture string
		NCPU                          int
		MemTotal                      int64
	}
	if err := json.Unmarshal(b, &info); err != nil {
		return "", fmt.Errorf("decode docker info: %w", err)
	}
	if info.Architecture == "" || info.NCPU == 0 || info.MemTotal == 0 {
		return "", fmt.Errorf("docker info lacks the architecture, CPUs or memory: %.200s", b)
	}
	return fmt.Sprintf("%s, %s, %d CPUs, %.1f GiB", info.OperatingSystem, info.Architecture, info.NCPU,
		float64(info.MemTotal)/(1<<30)), nil
}

// Table renders the measures as a markdown table, one row per server of o.Env.Servers.
func Table(o results.Ops) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Host: %s (docker daemon). Every server runs from its pinned image.\n\n", o.Host)
	fmt.Fprintf(&sb, "| Server | Image size | Cold start, median of %d | Memory after %d S02 calls, median of %d containers |\n",
		ColdStarts, MemCalls, MemSamples)
	sb.WriteString("|---|---|---|---|\n")
	for _, s := range o.Env.Servers {
		fmt.Fprintf(&sb, "| %s %s | %s | %s | %s |\n", s.Name, s.Version,
			cell(o.SizeMB[s.Name], "%.0f MB", false), cell(o.ColdStartMS[s.Name], "%.0f ms", false), cell(o.RSSMB[s.Name], "%.1f MB", true))
	}
	return sb.String()
}

// cell formats one measure, with its samples when asked.
func cell(m results.Measure, format string, samples bool) string {
	if m.Skipped {
		return "skipped (" + m.Note + ")"
	}
	s := fmt.Sprintf(format, m.Value)
	if samples && len(m.Samples) > 0 {
		parts := make([]string, len(m.Samples))
		for i, v := range m.Samples {
			parts[i] = strconv.FormatFloat(v, 'f', 1, 64)
		}
		s += " (" + strings.Join(parts, ", ") + ")"
	}
	return s
}
