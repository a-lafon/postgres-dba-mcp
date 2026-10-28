package safesql

import (
	"fmt"
	"path"
	"strings"
)

// pattern is a parsed function-name pattern: name or schema.name, both parts
// lowercase path.Match patterns. An empty schema matches any schema.
type pattern struct {
	raw    string
	schema string
	name   string
}

// Policy decides which function calls are allowed. Build it with NewPolicy.
type Policy struct {
	allow, deny []pattern
	builtin     []pattern
	warnings    []string
}

// NewPolicy builds a Policy from the user's allow and deny patterns
// (--allow-function, --deny-function). A pattern is name or schema.name with
// path.Match wildcards. The first matching rule decides: user deny, user
// allow, built-in denylist, otherwise allowed.
func NewPolicy(allow, deny []string) (*Policy, error) {
	p := &Policy{}
	var err error
	if p.allow, err = parsePatterns(allow); err != nil {
		return nil, fmt.Errorf("allow-function: %w", err)
	}
	if p.deny, err = parsePatterns(deny); err != nil {
		return nil, fmt.Errorf("deny-function: %w", err)
	}
	if p.builtin, err = parsePatterns(builtinDeny); err != nil {
		return nil, fmt.Errorf("built-in denylist: %w", err)
	}
	p.warnings = p.computeWarnings()
	return p, nil
}

// Warnings returns one line per allow pattern that overlaps the built-in
// denylist.
func (p *Policy) Warnings() []string {
	return append([]string(nil), p.warnings...)
}

func parsePatterns(raw []string) ([]pattern, error) {
	out := make([]pattern, 0, len(raw))
	for _, r := range raw {
		pt, err := parsePattern(r)
		if err != nil {
			return nil, err
		}
		out = append(out, pt)
	}
	return out, nil
}

func parsePattern(raw string) (pattern, error) {
	l := strings.ToLower(raw)
	parts := strings.Split(l, ".")
	if len(parts) > 2 {
		return pattern{}, fmt.Errorf("invalid pattern %q: at most one '.' (schema.name)", raw)
	}
	for _, part := range parts {
		if part == "" {
			return pattern{}, fmt.Errorf("invalid pattern %q: empty name part", raw)
		}
		if _, err := path.Match(sanitize(part), ""); err != nil {
			return pattern{}, fmt.Errorf("invalid pattern %q: %w", raw, err)
		}
	}
	pt := pattern{raw: raw, name: parts[len(parts)-1]}
	if len(parts) == 2 {
		pt.schema = parts[0]
	}
	return pt, nil
}

// sanitize makes '/' an ordinary character for path.Match, so that a quoted
// name containing '/' cannot slip past a '*'.
func sanitize(s string) string { return strings.ReplaceAll(s, "/", "\x00") }

func match(pat, s string) bool {
	ok, _ := path.Match(sanitize(pat), sanitize(s))
	return ok
}

// matches reports whether the pattern covers the call. A qualified pattern
// meets an unqualified call as a deny rule only: the call may resolve to that
// schema through search_path, so denial fails closed and allowance never
// applies.
func (pt pattern) matches(c call, deny bool) bool {
	if !match(pt.name, c.name) {
		return false
	}
	if pt.schema == "" {
		return true
	}
	if c.schema == "" {
		return deny
	}
	return match(pt.schema, c.schema)
}

// verdict is what the name rules say about a call.
type verdict int

const (
	// undecided means no rule named the call: the volatility gate decides.
	undecided verdict = iota
	denied
	allowed
)

// decide applies the name rules in order: user deny, user allow, built-in
// denylist. A call no rule names is undecided.
func (p *Policy) decide(c call) verdict {
	for _, pt := range p.deny {
		if pt.matches(c, true) {
			return denied
		}
	}
	for _, pt := range p.allow {
		if pt.matches(c, false) {
			return allowed
		}
	}
	for _, pt := range p.builtin {
		if match(pt.name, c.name) {
			return denied
		}
	}
	return undecided
}

// refuses reports whether the policy refuses the call on its name alone.
func (p *Policy) refuses(c call) bool { return p.decide(c) == denied }

func (p *Policy) computeWarnings() []string {
	var out []string
	for _, a := range p.allow {
		overlap := false
		for _, b := range p.builtin {
			// A pattern can be probed with the other's text, in both directions.
			if match(a.name, b.name) || match(b.name, a.name) {
				overlap = true
				break
			}
		}
		if !overlap {
			continue
		}
		w := fmt.Sprintf("WARNING: restricted mode: --allow-function=%s re-enables functions denied by default", a.raw)
		if match(a.name, "set_config") {
			w += " (callers can then change statement_timeout)"
		}
		out = append(out, w)
	}
	return out
}
