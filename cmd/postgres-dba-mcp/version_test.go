package main

import (
	"runtime/debug"
	"testing"
)

func TestEffectiveVersion(t *testing.T) {
	info := func(v string) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) { return &debug.BuildInfo{Main: debug.Module{Version: v}}, true }
	}
	tests := []struct {
		name string
		ldfl string
		read func() (*debug.BuildInfo, bool)
		want string
	}{
		{"ldflags wins", "1.2.3", info("v9.9.9"), "1.2.3"},
		{"module version", "dev", info("v0.1.0"), "v0.1.0"},
		{"local build", "dev", info("(devel)"), "dev"},
		{"checkout build with pseudo-version", "dev", func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{
				Main:     debug.Module{Version: "v0.0.0-20261021160720-fff1aaa1354d+dirty"},
				Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "fff1aaa"}},
			}, true
		}, "dev"},
		{"empty version", "dev", info(""), "dev"},
		{"no build info", "dev", func() (*debug.BuildInfo, bool) { return nil, false }, "dev"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := effectiveVersion(tt.ldfl, tt.read); got != tt.want {
				t.Errorf("effectiveVersion(%q) = %q, want %q", tt.ldfl, got, tt.want)
			}
		})
	}
}
