# Parser selection

Selected parser: `github.com/wasilibs/go-pgquery` (libpg_query 17). It is imported only by `parser.go`. The default build uses its WASM backend (compiled once at start by wazero); the build tag `pgquery_cgo` switches to its cgo backend (the same libpg_query compiled natively), which the Linux release binaries and the image use.

Measured on darwin/arm64 (Apple M1 Pro), Go 1.27.2, `CGO_ENABLED=0`, binaries built with `-ldflags="-s -w"`. Stand-in program: `internal/safesql/cmd/startup` (build tag `spike`, removed at the end of the change; it imports `safesql`, calls `Warm`, prints `ready`).

## go-pgquery (v0.0.0-20261020014038-c50e9f18754b)

| Criterion | Result | Limit |
|---|---|---|
| Start-up until `ready` (stand-in program, wall clock, 5 runs) | 4.3 to 5.2 ms, one outlier at 18.5 ms | < 50 ms |
| `Warm()` call | 21 to 63 µs | < 5 ms |
| First parse in a cold process (waits for the background WASM compilation) | 612 to 628 ms | not bounded by the spec; hidden by `Warm` called in `Register` |
| Parse time after warm-up, `BenchmarkParse` over the legitimate corpus | 40 µs per parse (mean, 2000 iterations; 179 KB and 266 allocs per parse) | median < 5 ms |
| Median `Check` after warm-up (stand-in, a join and group-by query, 200 runs) | 50 to 53 µs | median < 5 ms |
| Stand-in binary size, stripped (an empty Go program is 1.2 MB) | 11.05 MB darwin/arm64, so about 9.9 MB added | reported only |

Cross-builds with `CGO_ENABLED=0`:

| Target | Result | Stand-in size |
|---|---|---|
| darwin/amd64 | ok | 11.43 MB |
| darwin/arm64 | ok | 11.05 MB |
| linux/amd64 | ok | 11.21 MB |
| linux/arm64 | ok | 10.81 MB |
| windows/amd64 | ok | 11.51 MB |

Every criterion passes, so the fallback `pganalyze/pg_query_go/v6` was not measured.

Notes:
- The WASM compilation costs about 0.6 s of CPU once per process. It must start at boot (`Warm`), otherwise the first tool call pays it.
- go-pgquery's parse errors expose the cursor position only through the `Cursorpos` field of an internal type; `parser.go` reads it by reflection. If the field disappears, the message loses ` (position <n>)` and nothing else breaks.

## cgo backend (Linux release)

The WASM backend has a memory cost the criteria above do not capture: wazero compiles the module at the first parse and the process peaks near 290 MB (VmHWM, linux/arm64, 3 runs: 289 to 291 MB), so a container with `--memory=256m` is OOM-killed at start. With `-tags pgquery_cgo` (linux/arm64, static musl, same driver):

| | WASM (default) | cgo (`pgquery_cgo`) |
|---|---|---|
| VmHWM at the first call | 289 to 291 MB | 19 to 21 MB |
| Start to first call | 694 to 1067 ms | 32 to 83 ms |
| Parse after warm-up (`BenchmarkParse`) | 40 to 42 µs | 20 to 21 µs |
| Binary size | 19.3 MB | 17.6 MB |
| Survives `docker run --memory=64m` | no (also not at 128 and 256 MB) | yes (3 of 3) |

The release builds the Linux binaries this way (`.goreleaser.yaml`: `CGO_ENABLED=1`, `zig cc -target <arch>-linux-musl`, `-tags pgquery_cgo,netgo,osusergo`, fully static). darwin and windows keep the WASM backend with `CGO_ENABLED=0`: no container limit there, and zig cannot link darwin cgo binaries. `go install` builds the WASM backend; with a C compiler, `CGO_ENABLED=1 go install -tags pgquery_cgo,netgo,osusergo` builds the cgo one. The corpus tests pass with both tags.

CGO required: no (default build; the Linux release builds use cgo for memory, see above)
