package qda

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"unicode"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

type Target struct {
	Domain     string
	Input      string
	LineNumber int
}

type SkippedInput struct {
	Input      string `json:"input"`
	LineNumber int    `json:"line_number"`
	Reason     string `json:"reason"`
}

var idnaProfile = idna.New(idna.MapForLookup(), idna.StrictDomainName(false), idna.Transitional(false))

func NormalizeDomain(value string) (string, error) {
	host, err := extractHost(value)
	if err != nil {
		return "", err
	}

	host = strings.TrimSuffix(strings.TrimPrefix(host, "*."), ".")
	if strings.HasPrefix(host, "www.") && strings.Count(host, ".") >= 2 {
		host = strings.TrimPrefix(host, "www.")
	}

	ascii, err := idnaProfile.ToASCII(host)
	if err != nil {
		return "", fmt.Errorf("invalid IDNA domain: %w", err)
	}
	ascii = strings.ToLower(ascii)

	if err := validateDomainSyntax(ascii); err != nil {
		return "", err
	}

	return ascii, nil
}

func RegistrableDomain(value string) (string, error) {
	domain, err := NormalizeDomain(value)
	if err != nil {
		return "", err
	}

	registrable, err := publicsuffix.EffectiveTLDPlusOne(domain)
	if err != nil {
		return "", fmt.Errorf("cannot determine public suffix: %w", err)
	}
	return registrable, nil
}

func NormalizeTLD(value string) (string, error) {
	tld := strings.TrimSpace(strings.ToLower(value))
	tld = strings.TrimPrefix(tld, ".")
	tld = strings.TrimSuffix(tld, ".")
	if tld == "" {
		return "", errors.New("empty TLD")
	}

	ascii, err := idnaProfile.ToASCII(tld)
	if err != nil {
		return "", fmt.Errorf("invalid IDNA TLD: %w", err)
	}
	if strings.ContainsAny(ascii, "/?#:@") {
		return "", errors.New("TLD must not contain URL separators")
	}

	labels := strings.Split(ascii, ".")
	for _, label := range labels {
		if err := validateLabel(label); err != nil {
			return "", err
		}
	}
	return ascii, nil
}

func NormalizeWord(value string) (string, error) {
	word := strings.TrimSpace(strings.ToLower(value))
	if word == "" {
		return "", errors.New("empty word")
	}
	if strings.ContainsAny(word, ".:/?#@") {
		return "", errors.New("word contains domain or URL separators")
	}
	if strings.IndexFunc(word, unicode.IsSpace) >= 0 {
		return "", errors.New("word contains whitespace")
	}

	ascii, err := idnaProfile.ToASCII(word)
	if err != nil {
		return "", fmt.Errorf("invalid IDNA word: %w", err)
	}
	if err := validateLabel(ascii); err != nil {
		return "", err
	}
	return ascii, nil
}

func BuildDomainFromWord(word string, tld string) (string, error) {
	normalizedWord, err := NormalizeWord(word)
	if err != nil {
		return "", err
	}
	normalizedTLD, err := NormalizeTLD(tld)
	if err != nil {
		return "", err
	}
	return NormalizeDomain(normalizedWord + "." + normalizedTLD)
}

func extractHost(value string) (string, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return "", errors.New("empty input")
	}
	if strings.IndexFunc(raw, unicode.IsSpace) >= 0 {
		return "", errors.New("input contains whitespace")
	}

	lower := strings.ToLower(raw)
	if strings.Contains(lower, "://") {
		parsed, err := url.Parse(raw)
		if err != nil {
			return "", fmt.Errorf("invalid URL: %w", err)
		}
		if parsed.Host == "" {
			return "", errors.New("URL does not contain a host")
		}
		return normalizeHostPort(parsed.Host)
	}

	cut := raw
	for _, sep := range []string{"/", "?", "#"} {
		if i := strings.Index(cut, sep); i >= 0 {
			cut = cut[:i]
		}
	}
	return normalizeHostPort(cut)
}

func normalizeHostPort(host string) (string, error) {
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "" {
		return "", errors.New("empty host")
	}
	if strings.Contains(host, "@") {
		return "", errors.New("userinfo is not accepted")
	}

	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	} else if strings.Count(host, ":") == 1 {
		candidate, port, ok := strings.Cut(host, ":")
		if ok && port != "" && allDigits(port) {
			host = candidate
		}
	}

	host = strings.Trim(host, "[]")
	if net.ParseIP(host) != nil {
		return "", errors.New("IP addresses are not domains")
	}
	return host, nil
}

func validateDomainSyntax(domain string) error {
	if len(domain) > 253 {
		return errors.New("domain is longer than 253 characters")
	}
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return errors.New("domain must contain at least one dot")
	}
	for _, label := range labels {
		if err := validateLabel(label); err != nil {
			return err
		}
	}
	return nil
}

func validateLabel(label string) error {
	if label == "" {
		return errors.New("empty domain label")
	}
	if len(label) > 63 {
		return fmt.Errorf("domain label %q is longer than 63 octets", label)
	}
	if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
		return fmt.Errorf("domain label %q starts or ends with hyphen", label)
	}
	for _, r := range label {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return fmt.Errorf("domain label %q contains an invalid character", label)
	}
	return nil
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
