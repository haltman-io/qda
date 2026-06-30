package qda

import "testing"

func TestNormalizeDomainFromURLAndIDNA(t *testing.T) {
	got, err := NormalizeDomain("https://WWW.ex\u00e4mple.com/path?q=1")
	if err != nil {
		t.Fatal(err)
	}
	want := "xn--exmple-cua.com"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRegistrableDomainUsesPublicSuffix(t *testing.T) {
	got, err := RegistrableDomain("foo.example.com.br")
	if err != nil {
		t.Fatal(err)
	}
	want := "example.com.br"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRejectInvalidDomainLabel(t *testing.T) {
	if _, err := NormalizeDomain("-bad.example"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestDoesNotStripWWWWhenItWouldBreakDomain(t *testing.T) {
	got, err := NormalizeDomain("www.com")
	if err != nil {
		t.Fatal(err)
	}
	if got != "www.com" {
		t.Fatalf("got %q", got)
	}
}

func TestBuildDomainFromWord(t *testing.T) {
	got, err := BuildDomainFromWord("brand", ".com.br")
	if err != nil {
		t.Fatal(err)
	}
	if got != "brand.com.br" {
		t.Fatalf("got %q", got)
	}
}
