package generate

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCombinationsCount(t *testing.T) {
	var buf bytes.Buffer
	count, err := Combinations(&buf, 2, "digits")
	if err != nil {
		t.Fatal(err)
	}
	if count != 100 {
		t.Fatalf("count = %d, want 100", count)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 100 {
		t.Fatalf("lines = %d", len(lines))
	}
	if lines[0] != "00" || lines[99] != "99" {
		t.Fatalf("first=%q last=%q", lines[0], lines[99])
	}
}

func TestCombinationsLetters(t *testing.T) {
	var buf bytes.Buffer
	count, err := Combinations(&buf, 1, "letters")
	if err != nil {
		t.Fatal(err)
	}
	if count != 26 {
		t.Fatalf("count = %d", count)
	}
}

func TestCombinationsRejectsBadCharset(t *testing.T) {
	var buf bytes.Buffer
	if _, err := Combinations(&buf, 2, "nope"); err == nil {
		t.Fatal("expected error")
	}
}

func TestMergeDedupesAndSorts(t *testing.T) {
	dir := t.TempDir()
	fileA := filepath.Join(dir, "a.txt")
	fileB := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(fileA, []byte("bbbb\naa\n# comment\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileB, []byte("aa\nc\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	count, err := Merge(&buf, []string{fileA, fileB}, true)
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("count = %d", count)
	}
	got := strings.Split(strings.TrimSpace(buf.String()), "\n")
	want := []string{"c", "aa", "bbbb"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestExpandDomains(t *testing.T) {
	var buf bytes.Buffer
	count, err := ExpandDomains(&buf, []string{"bug"}, []string{"net", "org"})
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("count = %d", count)
	}
	got := strings.TrimSpace(buf.String())
	if got != "bug.net\nbug.org" {
		t.Fatalf("got %q", got)
	}
}
