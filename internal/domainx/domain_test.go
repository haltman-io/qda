package domainx

import (
	"strings"
	"testing"

	"qda/internal/types"
)

func TestNormalizeDomain(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "plain", input: "Example.COM", want: "example.com"},
		{name: "url", input: "https://Example.com/path?q=1", want: "example.com"},
		{name: "url with port", input: "https://example.com:8443/x", want: "example.com"},
		{name: "host with port", input: "example.com:443", want: "example.com"},
		{name: "wildcard", input: "*.example.com", want: "example.com"},
		{name: "www prefix", input: "www.example.com", want: "example.com"},
		{name: "trailing dot", input: "example.com.", want: "example.com"},
		{name: "idna", input: "bücher.de", want: "xn--bcher-kva.de"},
		{name: "empty", input: "", wantErr: true},
		{name: "whitespace", input: "exa mple.com", wantErr: true},
		{name: "ip", input: "127.0.0.1", wantErr: true},
		{name: "single label", input: "localhost", wantErr: true},
		{name: "bad char", input: "exa_mple.com", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NormalizeDomain(test.input)
			if test.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}

func TestRegistrableDomain(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "sub.example.com", want: "example.com"},
		{input: "example.com.br", want: "example.com.br"},
		{input: "deep.sub.example.co.uk", want: "example.co.uk"},
	}
	for _, test := range tests {
		got, err := RegistrableDomain(test.input)
		if err != nil {
			t.Fatalf("%s: %v", test.input, err)
		}
		if got != test.want {
			t.Fatalf("%s: got %q, want %q", test.input, got, test.want)
		}
	}
}

func TestNormalizeWord(t *testing.T) {
	if word, err := NormalizeWord("Kernel"); err != nil || word != "kernel" {
		t.Fatalf("got %q, %v", word, err)
	}
	if _, err := NormalizeWord("has space"); err == nil {
		t.Fatal("expected error for whitespace")
	}
	if _, err := NormalizeWord("with.dot"); err == nil {
		t.Fatal("expected error for dot")
	}
}

func TestBuildDomainFromWord(t *testing.T) {
	domain, err := BuildDomainFromWord("bug", ".net")
	if err != nil {
		t.Fatal(err)
	}
	if domain != "bug.net" {
		t.Fatalf("got %q", domain)
	}
}

func TestLoadExpandsWordsAcrossTLDsInPriorityOrder(t *testing.T) {
	input := strings.NewReader("kernel\nbug\n# comment\nexample.org\nkernel\n")
	targets, skipped, err := Load(input, LoadOptions{TLDs: []string{"net", "org"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 0 {
		t.Fatalf("unexpected skipped: %+v", skipped)
	}
	want := []string{"kernel.net", "bug.net", "kernel.org", "bug.org", "example.org"}
	if len(targets) != len(want) {
		t.Fatalf("got %d targets %v, want %d", len(targets), domainsOf(targets), len(want))
	}
	for i, target := range targets {
		if target.Domain != want[i] {
			t.Fatalf("position %d: got %q, want %q (full: %v)", i, target.Domain, want[i], domainsOf(targets))
		}
	}
}

func TestLoadShortFirst(t *testing.T) {
	input := strings.NewReader("aaaa\nb\ncc\n")
	targets, _, err := Load(input, LoadOptions{TLDs: []string{"net"}, ShortFirst: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"b.net", "cc.net", "aaaa.net"}
	for i, target := range targets {
		if target.Domain != want[i] {
			t.Fatalf("position %d: got %q, want %q", i, target.Domain, want[i])
		}
	}
}

func TestLoadWordFirst(t *testing.T) {
	input := strings.NewReader("aa\nbb\n")
	targets, _, err := Load(input, LoadOptions{TLDs: []string{"net", "org"}, WordFirst: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"aa.net", "aa.org", "bb.net", "bb.org"}
	for i, target := range targets {
		if target.Domain != want[i] {
			t.Fatalf("position %d: got %q, want %q", i, target.Domain, want[i])
		}
	}
}

func TestLoadSkipsInvalid(t *testing.T) {
	input := strings.NewReader("okword\nhas space\n")
	_, skipped, err := Load(input, LoadOptions{TLDs: []string{"net"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 1 || skipped[0].Input != "has space" {
		t.Fatalf("got %+v", skipped)
	}
}

func domainsOf(targets []types.Target) []string {
	out := make([]string, 0, len(targets))
	for _, target := range targets {
		out = append(out, target.Domain)
	}
	return out
}
