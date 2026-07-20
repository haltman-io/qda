package cli

import (
	"bytes"
	"flag"
	"strings"
	"testing"
	"time"
)

func TestDurationFlagHelpDoesNotPanic(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	var buf bytes.Buffer
	fs.SetOutput(&buf)

	timeout := 12 * time.Second
	rate := 500 * time.Millisecond
	durationVar(fs, &timeout, "timeout", "HTTP timeout")
	durationVar(fs, &rate, "rate-limit", "Minimum delay")

	// PrintDefaults probes zero Value.String(); must not panic.
	fs.PrintDefaults()
	out := buf.String()
	if !strings.Contains(out, "-timeout") || !strings.Contains(out, "-rate-limit") {
		t.Fatalf("help missing flags:\n%s", out)
	}
	if strings.Contains(out, "panic") {
		t.Fatalf("unexpected panic text:\n%s", out)
	}
}

func TestDurationFlagSet(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	timeout := 12 * time.Second
	durationVar(fs, &timeout, "timeout", "HTTP timeout")
	if err := fs.Parse([]string{"-timeout", "3s"}); err != nil {
		t.Fatal(err)
	}
	if timeout != 3*time.Second {
		t.Fatalf("timeout = %s, want 3s", timeout)
	}
}
