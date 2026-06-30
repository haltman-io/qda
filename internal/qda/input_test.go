package qda

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadTargetsExpandsWordsAndKeepsDomains(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "words.txt")
	data := "brand\nfoo.example.com.br\nbad value\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	targets, skipped, err := LoadTargets(path, []string{"com", "net"})
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]bool{}
	for _, target := range targets {
		got[target.Domain] = true
	}
	for _, want := range []string{"brand.com", "brand.net", "example.com.br"} {
		if !got[want] {
			t.Fatalf("missing target %q in %#v", want, targets)
		}
	}
	if len(skipped) != 1 {
		t.Fatalf("got %d skipped, want 1", len(skipped))
	}
}
