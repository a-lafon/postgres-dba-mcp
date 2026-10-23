package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestHelpPrintsUsageAndExitsZero(t *testing.T) {
	for _, arg := range []string{"--help", "-h", "-help"} {
		t.Run(arg, func(t *testing.T) {
			var out, errOut bytes.Buffer
			code := run(context.Background(), []string{arg}, func(string) string { return "" }, &out, &errOut)
			if code != 0 || errOut.Len() != 0 || out.String() != usage {
				t.Errorf("code %d, stdout %q, stderr %q", code, out.String(), errOut.String())
			}
		})
	}
}

func TestUsageListsEveryFlag(t *testing.T) {
	for _, name := range append(registeredFlags(t), "help") {
		if !strings.Contains(usage, "--"+name) {
			t.Errorf("flag --%s is not in the usage text", name)
		}
	}
}
