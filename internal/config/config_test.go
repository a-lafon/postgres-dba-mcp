package config

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		env  map[string]string
		want string
	}{
		{"no URI", nil, nil, "no database URI: pass it as an argument or set DATABASE_URI"},
		{"function pattern", []string{"--deny-function=a.b.c", "postgres://u@h/db"}, nil, `--deny-function: invalid pattern "a.b.c"`},
		{"bad glob", []string{"--allow-function=[", "postgres://u@h/db"}, nil, `--allow-function: invalid pattern "["`},
		{"empty part", []string{"--allow-function=a.", "postgres://u@h/db"}, nil, `--allow-function: invalid pattern "a."`},
		{"tools group", []string{"--tools=schema,foo", "postgres://u@h/db"}, nil,
			`--tools: unknown group "foo" (valid: schema, query, explain, stats, health, index, locks, all)`},
		{"rows above limit", []string{"--max-rows=5000", "--max-rows-limit=1000", "postgres://u@h/db"}, nil,
			"--max-rows: 5000 exceeds --max-rows-limit 1000"},
		{"cell above limit", []string{"--max-cell=500", "--max-cell-limit=400", "postgres://u@h/db"}, nil,
			"--max-cell: 500 exceeds --max-cell-limit 400"},
		{"access mode", []string{"--access-mode=open", "postgres://u@h/db"}, nil, "--access-mode: must be restricted or unrestricted"},
		{"transport", []string{"--transport=websocket", "postgres://u@h/db"}, nil, `--transport: unknown transport "websocket" (valid: stdio, streamable-http, sse)`},
		{"alias host conflict", []string{"--transport=sse", "--host=127.0.0.1", "--sse-host=0.0.0.0", "--allow-remote", "postgres://u@h/db"}, nil, "--sse-host conflicts with --host"},
		{"alias port conflict", []string{"--transport=sse", "--port=8000", "--sse-port=9000", "postgres://u@h/db"}, nil, "--sse-port conflicts with --port"},
		{"port zero", []string{"--transport=sse", "--port=0", "postgres://u@h/db"}, nil, "--port: 0 is not between 1 and 65535"},
		{"port high", []string{"--transport=sse", "--port=70000", "postgres://u@h/db"}, nil, "--port: 70000 is not between 1 and 65535"},
		{"bad host", []string{"--transport=sse", "--host=a b", "--allow-remote", "postgres://u@h/db"}, nil, `--host: "a b" is not an IP address or a host name`},
		{"wildcard bind", []string{"--transport=streamable-http", "--host=0.0.0.0", "postgres://u@h/db"}, nil, "--host 0.0.0.0: binding a non-loopback address requires --allow-remote"},
		{"name bind", []string{"--transport=streamable-http", "--host=db.internal", "postgres://u@h/db"}, nil, "--host db.internal: binding a non-loopback address requires --allow-remote"},
		{"bad origin", []string{"--transport=sse", "--allowed-origins=example.com", "postgres://u@h/db"}, nil, `--allowed-origins: invalid origin "example.com"`},
		{"max-bytes low", []string{"--max-bytes=10", "postgres://u@h/db"}, nil, "--max-bytes: 10 is below the minimum 1024"},
		{"max-cell low", []string{"--max-cell=1", "postgres://u@h/db"}, nil, "--max-cell: 1 is below the minimum 16"},
		{"not an integer", []string{"--max-rows=abc", "postgres://u@h/db"}, nil, `--max-rows: "abc" is not an integer`},
		{"timeout zero", []string{"--statement-timeout=0s", "postgres://u@h/db"}, nil, "--statement-timeout: must be greater than 0"},
		{"timeout syntax", []string{"--statement-timeout=5", "postgres://u@h/db"}, nil, "--statement-timeout:"},
		{"unknown flag", []string{"--nope", "postgres://u@h/db"}, nil, "--nope: unknown flag"},
		{"missing value", []string{"postgres://u@h/db", "--tools"}, nil, "--tools: needs a value"},
		{"two URIs", []string{"postgres://u@h/db", "postgres://u@h/db2"}, nil, `unexpected argument "postgres://u@h/db2"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.args, envOf(tt.env))
			if err == nil || !strings.HasPrefix(err.Error(), tt.want) {
				t.Fatalf("Parse() error = %v, want prefix %q", err, tt.want)
			}
		})
	}
}

func TestParseHTTP(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want Config // only the HTTP fields are compared
	}{
		{"defaults", []string{"--transport=streamable-http"}, Config{Host: "127.0.0.1", Port: 8000}},
		{"crystaldba sse flags", []string{"--transport=sse", "--sse-port=9000"}, Config{Host: "127.0.0.1", Port: 9000}},
		{"same alias value", []string{"--transport=sse", "--host=localhost", "--sse-host=localhost"}, Config{Host: "localhost", Port: 8000}},
		{"loopback range and v6", []string{"--transport=sse", "--host=127.0.0.2"}, Config{Host: "127.0.0.2", Port: 8000}},
		{"v6 loopback", []string{"--transport=sse", "--host=::1"}, Config{Host: "::1", Port: 8000}},
		{"remote with lists", []string{"--transport=streamable-http", "--host=0.0.0.0", "--allow-remote", "--allowed-hosts=a.internal,b:81", "--allowed-hosts=c", "--allowed-origins=https://x.example"},
			Config{Host: "0.0.0.0", Port: 8000, AllowRemote: true, AllowedHosts: []string{"a.internal", "b:81", "c"}, AllowedOrigins: []string{"https://x.example"}}},
		{"stdio ignores HTTP flags", []string{"--host=0.0.0.0", "--port=0"}, Config{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Parse(append(tt.args, "postgres://u@h/db"), envOf(nil))
			if err != nil {
				t.Fatal(err)
			}
			got := Config{Host: c.Host, Port: c.Port, AllowRemote: c.AllowRemote, AllowedHosts: c.AllowedHosts, AllowedOrigins: c.AllowedOrigins}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("HTTP config = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseOK(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		env   map[string]string
		check func(t *testing.T, c *Config)
	}{
		{"environment wins", []string{"postgres://b@h2/db2"}, map[string]string{"DATABASE_URI": "postgres://a@h1/db1"},
			func(t *testing.T, c *Config) {
				if c.Redacted() != "a@h1:5432/db1" {
					t.Errorf("Redacted() = %q", c.Redacted())
				}
			}},
		{"flags after the URI", []string{"postgres://u@h/db", "--tools=schema"}, nil,
			func(t *testing.T, c *Config) {
				if !reflect.DeepEqual(c.Tools, []string{"schema"}) || c.DSN != "postgres://u@h/db" {
					t.Errorf("Tools = %v, DSN = %q", c.Tools, c.DSN)
				}
			}},
		{"flags around the URI", []string{"--max-rows", "5", "postgres://u@h/db", "--max-bytes=2048"}, nil,
			func(t *testing.T, c *Config) {
				if c.MaxRows != 5 || c.MaxBytes != 2048 {
					t.Errorf("MaxRows = %d, MaxBytes = %d", c.MaxRows, c.MaxBytes)
				}
			}},
		{"defaults", []string{"postgres://u@h/db"}, nil,
			func(t *testing.T, c *Config) {
				want := Config{DSN: "postgres://u@h/db", AccessMode: "restricted", Transport: "stdio", Tools: Groups,
					MaxRows: 100, MaxRowsLimit: 1000, MaxBytes: 32768, MaxCell: 200, MaxCellLimit: 10000, StatementTimeout: 30 * time.Second}
				if !reflect.DeepEqual(*c, want) {
					t.Errorf("config = %+v, want %+v", *c, want)
				}
			}},
		{"repeatable functions", []string{"--allow-function=pg_*", "--allow-function=public.f", "--deny-function=x", "postgres://u@h/db"}, nil,
			func(t *testing.T, c *Config) {
				if !reflect.DeepEqual(c.AllowFunctions, []string{"pg_*", "public.f"}) || !reflect.DeepEqual(c.DenyFunctions, []string{"x"}) {
					t.Errorf("allow = %v, deny = %v", c.AllowFunctions, c.DenyFunctions)
				}
			}},
		{"version without URI", []string{"--version"}, nil,
			func(t *testing.T, c *Config) {
				if !c.ShowVersion {
					t.Error("ShowVersion = false")
				}
			}},
		{"tools all", []string{"--tools=all", "postgres://u@h/db"}, nil,
			func(t *testing.T, c *Config) {
				if !c.ToolEnabled("index") || !c.ToolEnabled("schema") {
					t.Errorf("Tools = %v", c.Tools)
				}
			}},
		{"key/value DSN", []string{"host=h port=5433 user=u dbname=d"}, nil,
			func(t *testing.T, c *Config) {
				if c.Redacted() != "u@h:5433/d" {
					t.Errorf("Redacted() = %q", c.Redacted())
				}
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Parse(tt.args, envOf(tt.env))
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			tt.check(t, c)
		})
	}
}

func TestRedactedHidesPassword(t *testing.T) {
	for _, dsn := range []string{"postgres://u:s3cret@h:5433/db", "host=h user=u password=s3cret dbname=db"} {
		c := &Config{DSN: dsn}
		if got := c.Redacted(); strings.Contains(got, "s3cret") || !strings.HasPrefix(got, "u@h") {
			t.Errorf("Redacted(%q) = %q", dsn, got)
		}
	}
	if got := (&Config{DSN: "postgres://u:s3cret@h:notaport/db"}).Redacted(); strings.Contains(got, "s3cret") {
		t.Errorf("Redacted() of an invalid DSN = %q", got)
	}
}
