package banner

import (
	"strings"
	"testing"
)

func TestTextColorAndPlain(t *testing.T) {
	colored := Text(true)
	plain := Text(false)
	if !strings.Contains(colored, "\x1b[") {
		t.Fatal("colored banner missing ANSI escapes")
	}
	if strings.Contains(plain, "\x1b[") {
		t.Fatal("plain banner should not contain ANSI escapes")
	}
	for _, s := range []string{colored, plain} {
		if !strings.Contains(s, "haltman.io") || !strings.Contains(s, "https://github.com/haltman-io/qda") {
			t.Fatalf("missing developer/repo footer: %q", s[:min(80, len(s))])
		}
		if !strings.Contains(s, "quick domain availability") {
			t.Fatal("missing tagline")
		}
	}
}
