// Package config turns the command line and the environment into a validated Config.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// Groups lists the tool groups, in the order used by error messages.
var Groups = []string{"schema", "query", "explain", "stats", "health", "index"}

// Access modes.
const (
	// Restricted is the default access mode: read-only, with the SQL safety checks.
	Restricted = "restricted"
	// Unrestricted lets tools modify the database.
	Unrestricted = "unrestricted"
)

// Transports.
const (
	// Stdio serves MCP over stdin and stdout.
	Stdio = "stdio"
	// StreamableHTTP serves MCP streamable HTTP on /mcp.
	StreamableHTTP = "streamable-http"
	// SSE serves the deprecated MCP SSE transport on /sse.
	SSE = "sse"
)

// Config is the effective configuration of one server process.
type Config struct {
	DSN                           string
	AccessMode                    string // Restricted or Unrestricted
	Transport                     string // Stdio, StreamableHTTP or SSE
	Host                          string // HTTP transports: bind address
	Port                          int    // HTTP transports: bind port
	AllowRemote                   bool   // HTTP transports: allow a non-loopback Host
	AllowedHosts, AllowedOrigins  []string
	Tools                         []string // enabled groups, "all" expanded
	MaxRows, MaxRowsLimit         int
	MaxBytes, MaxCell             int
	MaxCellLimit                  int
	StatementTimeout              time.Duration
	AllowFunctions, DenyFunctions []string
	ShowVersion                   bool // --version: Parse returned before any other validation
}

// ToolEnabled reports whether the tool group is enabled by --tools.
func (c *Config) ToolEnabled(group string) bool {
	for _, g := range c.Tools {
		if g == group {
			return true
		}
	}
	return false
}

// Redacted returns the target as user@host:port/db, without the password.
func (c *Config) Redacted() string {
	pc, err := pgconn.ParseConfig(c.DSN)
	if err != nil {
		return "(unparsable database URI)"
	}
	return fmt.Sprintf("%s@%s:%d/%s", pc.User, pc.Host, pc.Port, pc.Database)
}

// Parse reads the flags in args (without the program name) and the environment.
// The returned error text is meant to be printed after "error: ".
func Parse(args []string, env func(string) string) (*Config, error) {
	c := &Config{}
	fs := flag.NewFlagSet("postgres-dba-mcp", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var tools string
	maxRows := &intValue{v: 100}
	maxRowsLimit := &intValue{v: 1000}
	maxBytes := &intValue{v: 32768}
	maxCell := &intValue{v: 200}
	maxCellLimit := &intValue{v: 10000}
	timeout := &durationValue{v: 30 * time.Second}
	var allow, deny listValue
	var allowedHosts, allowedOrigins csvValue
	var host, sseHost string
	port := &intValue{v: 8000}
	ssePort := &intValue{}

	fs.StringVar(&c.AccessMode, "access-mode", Restricted, "")
	fs.StringVar(&c.Transport, "transport", Stdio, "")
	fs.StringVar(&host, "host", "", "")
	fs.StringVar(&sseHost, "sse-host", "", "")
	fs.Var(port, "port", "")
	fs.Var(ssePort, "sse-port", "")
	fs.BoolVar(&c.AllowRemote, "allow-remote", false, "")
	fs.Var(&allowedHosts, "allowed-hosts", "")
	fs.Var(&allowedOrigins, "allowed-origins", "")
	fs.StringVar(&tools, "tools", "all", "")
	fs.Var(maxRows, "max-rows", "")
	fs.Var(maxRowsLimit, "max-rows-limit", "")
	fs.Var(maxBytes, "max-bytes", "")
	fs.Var(maxCell, "max-cell", "")
	fs.Var(maxCellLimit, "max-cell-limit", "")
	fs.Var(timeout, "statement-timeout", "")
	fs.Var(&allow, "allow-function", "")
	fs.Var(&deny, "deny-function", "")
	fs.BoolVar(&c.ShowVersion, "version", false, "")

	// The standard flag package stops at the first positional argument: parse again after each one.
	var positional []string
	for rest := args; ; {
		if err := fs.Parse(rest); err != nil {
			return nil, flagError(err)
		}
		rest = fs.Args()
		if len(rest) == 0 {
			break
		}
		positional = append(positional, rest[0])
		rest = rest[1:]
	}
	if c.ShowVersion {
		return c, nil
	}

	switch {
	case env("DATABASE_URI") != "":
		c.DSN = env("DATABASE_URI")
	case len(positional) > 0:
		c.DSN = positional[0]
	default:
		return nil, errors.New("no database URI: pass it as an argument or set DATABASE_URI")
	}
	if len(positional) > 1 {
		return nil, fmt.Errorf("unexpected argument %q", positional[1])
	}

	if c.AccessMode != Restricted && c.AccessMode != Unrestricted {
		return nil, fmt.Errorf("--access-mode: must be %s or %s", Restricted, Unrestricted)
	}
	switch c.Transport {
	case Stdio:
	case StreamableHTTP, SSE:
		if err := c.resolveHTTP(host, sseHost, port, ssePort, allowedHosts, allowedOrigins); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("--transport: unknown transport %q (valid: %s, %s, %s)", c.Transport, Stdio, StreamableHTTP, SSE)
	}
	var err error
	if c.Tools, err = parseTools(tools); err != nil {
		return nil, err
	}
	c.MaxRows, c.MaxRowsLimit, c.MaxBytes, c.MaxCell, c.MaxCellLimit = maxRows.v, maxRowsLimit.v, maxBytes.v, maxCell.v, maxCellLimit.v
	c.StatementTimeout = timeout.v
	for _, chk := range []struct {
		name string
		v, n int
	}{{"max-rows", c.MaxRows, 1}, {"max-rows-limit", c.MaxRowsLimit, 1}, {"max-bytes", c.MaxBytes, 1024}, {"max-cell", c.MaxCell, 16}, {"max-cell-limit", c.MaxCellLimit, 16}} {
		if chk.v < chk.n {
			return nil, fmt.Errorf("--%s: %d is below the minimum %d", chk.name, chk.v, chk.n)
		}
	}
	if c.MaxRows > c.MaxRowsLimit {
		return nil, fmt.Errorf("--max-rows: %d exceeds --max-rows-limit %d", c.MaxRows, c.MaxRowsLimit)
	}
	if c.MaxCell > c.MaxCellLimit {
		return nil, fmt.Errorf("--max-cell: %d exceeds --max-cell-limit %d", c.MaxCell, c.MaxCellLimit)
	}
	if c.StatementTimeout <= 0 {
		return nil, errors.New("--statement-timeout: must be greater than 0")
	}
	for _, l := range []struct {
		flag string
		v    []string
	}{{"--allow-function", allow}, {"--deny-function", deny}} {
		for _, p := range l.v {
			if err := checkFunctionPattern(p); err != nil {
				return nil, fmt.Errorf("%s: %w", l.flag, err)
			}
		}
	}
	c.AllowFunctions, c.DenyFunctions = allow, deny
	return c, nil
}

var hostName = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$`)

// resolveHTTP applies the aliases, validates the HTTP flags and sets the HTTP fields of c.
func (c *Config) resolveHTTP(host, sseHost string, port, ssePort *intValue, hosts, origins []string) error {
	if host != "" && sseHost != "" && host != sseHost {
		return errors.New("--sse-host conflicts with --host")
	}
	if port.set && ssePort.set && port.v != ssePort.v {
		return errors.New("--sse-port conflicts with --port")
	}
	c.Host, c.Port = "127.0.0.1", port.v
	if sseHost != "" {
		c.Host = sseHost
	}
	if host != "" {
		c.Host = host
	}
	if ssePort.set {
		c.Port = ssePort.v
	}
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("--port: %d is not between 1 and 65535", c.Port)
	}
	if net.ParseIP(c.Host) == nil && !hostName.MatchString(c.Host) {
		return fmt.Errorf("--host: %q is not an IP address or a host name", c.Host)
	}
	if !c.AllowRemote && !IsLoopback(c.Host) {
		return fmt.Errorf("--host %s: binding a non-loopback address requires --allow-remote", c.Host)
	}
	for _, h := range hosts {
		if strings.TrimSpace(h) == "" {
			return errors.New("--allowed-hosts: empty entry")
		}
		c.AllowedHosts = append(c.AllowedHosts, strings.TrimSpace(h))
	}
	for _, o := range origins {
		u, err := url.Parse(strings.TrimSpace(o))
		if err != nil || u.Scheme == "" || u.Host == "" || u.Path != "" {
			return fmt.Errorf("--allowed-origins: invalid origin %q (want scheme://host[:port])", o)
		}
		c.AllowedOrigins = append(c.AllowedOrigins, strings.TrimSpace(o))
	}
	return nil
}

// IsLoopback reports whether host is the name localhost or an address of 127.0.0.0/8 or ::1.
func IsLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func parseTools(s string) ([]string, error) {
	var out []string
	for _, g := range strings.Split(s, ",") {
		g = strings.TrimSpace(g)
		if g == "all" {
			return append([]string(nil), Groups...), nil
		}
		found := false
		for _, v := range Groups {
			found = found || v == g
		}
		if !found {
			return nil, fmt.Errorf("--tools: unknown group %q (valid: %s, all)", g, strings.Join(Groups, ", "))
		}
		out = append(out, g)
	}
	return out, nil
}

// checkFunctionPattern applies the rule of the safe-SQL policy: at most one dot, non-empty parts, valid path.Match patterns.
func checkFunctionPattern(p string) error {
	parts := strings.Split(p, ".")
	if len(parts) > 2 {
		return fmt.Errorf("invalid pattern %q: at most one \".\" (schema.function)", p)
	}
	for _, part := range parts {
		if part == "" {
			return fmt.Errorf("invalid pattern %q: empty name", p)
		}
		if _, err := path.Match(part, ""); err != nil {
			return fmt.Errorf("invalid pattern %q: %w", p, err)
		}
	}
	return nil
}

var (
	invalidValue = regexp.MustCompile(`^invalid value ".*" for flag -(\S+): (.*)$`)
	undefined    = regexp.MustCompile(`^flag provided but not defined: -(\S+)$`)
	needsArg     = regexp.MustCompile(`^flag needs an argument: -(\S+)`)
)

// flagError rewrites an error of the flag package as "--name: reason".
func flagError(err error) error {
	s := err.Error()
	if m := invalidValue.FindStringSubmatch(s); m != nil {
		return fmt.Errorf("--%s: %s", m[1], m[2])
	}
	if m := undefined.FindStringSubmatch(s); m != nil {
		return fmt.Errorf("--%s: unknown flag", m[1])
	}
	if m := needsArg.FindStringSubmatch(s); m != nil {
		return fmt.Errorf("--%s: needs a value", m[1])
	}
	return err
}

type intValue struct {
	v   int
	set bool
}

func (i *intValue) String() string { return strconv.Itoa(i.v) }
func (i *intValue) Set(s string) error {
	n, err := strconv.Atoi(s)
	if err != nil {
		return fmt.Errorf("%q is not an integer", s)
	}
	i.v, i.set = n, true
	return nil
}

type durationValue struct{ v time.Duration }

func (d *durationValue) String() string { return d.v.String() }
func (d *durationValue) Set(s string) error {
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("%q is not a duration (for example 30s)", s)
	}
	d.v = v
	return nil
}

type listValue []string

func (l *listValue) String() string { return strings.Join(*l, ",") }
func (l *listValue) Set(s string) error {
	*l = append(*l, s)
	return nil
}

// csvValue is a repeatable flag whose values are comma lists.
type csvValue []string

func (l *csvValue) String() string { return strings.Join(*l, ",") }
func (l *csvValue) Set(s string) error {
	*l = append(*l, strings.Split(s, ",")...)
	return nil
}
