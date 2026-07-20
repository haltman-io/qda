package domainx

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"qda/internal/types"
)

// LoadOptions controls how input lines become targets.
type LoadOptions struct {
	// TLDs in priority order. Words are expanded across every TLD.
	TLDs []string
	// ShortFirst sorts words by length (shortest first) before expansion.
	ShortFirst bool
	// WordFirst iterates words in the outer loop and TLDs in the inner loop.
	// The default (false) prioritizes TLD classes: all words for the first
	// TLD are emitted before moving to the next TLD.
	WordFirst bool
}

// LoadFile reads targets from a file path.
func LoadFile(path string, opts LoadOptions) ([]types.Target, []types.SkippedInput, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open input: %w", err)
	}
	defer file.Close()
	return Load(file, opts)
}

// Load reads lines of words/domains from r and expands them into targets.
func Load(r io.Reader, opts LoadOptions) ([]types.Target, []types.SkippedInput, error) {
	normalizedTLDs, err := NormalizeTLDs(opts.TLDs)
	if err != nil {
		return nil, nil, err
	}

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	var words []string
	var domains []types.Target
	var skipped []types.SkippedInput
	seen := map[string]bool{}
	lineNumber := 0

	for scanner.Scan() {
		lineNumber++
		value := strings.TrimSpace(scanner.Text())
		if value == "" || strings.HasPrefix(value, "#") {
			continue
		}

		if looksLikeDomainOrURL(value) {
			domain, err := RegistrableDomain(value)
			if err != nil {
				skipped = append(skipped, types.SkippedInput{Input: value, LineNumber: lineNumber, Reason: err.Error()})
				continue
			}
			if !seen[domain] {
				domains = append(domains, types.Target{Domain: domain, Input: value, LineNumber: lineNumber})
				seen[domain] = true
			}
			continue
		}

		word, err := NormalizeWord(value)
		if err != nil {
			skipped = append(skipped, types.SkippedInput{Input: value, LineNumber: lineNumber, Reason: err.Error()})
			continue
		}
		words = append(words, word)
	}

	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("read input: %w", err)
	}

	words = dedupeWords(words)
	if opts.ShortFirst {
		sort.SliceStable(words, func(i, j int) bool {
			if len(words[i]) != len(words[j]) {
				return len(words[i]) < len(words[j])
			}
			return words[i] < words[j]
		})
	}

	targets := expandWords(words, normalizedTLDs, opts.WordFirst, seen)
	targets = append(targets, domains...)
	return targets, skipped, nil
}

// NormalizeTLDs validates a TLD list.
func NormalizeTLDs(tlds []string) ([]string, error) {
	out := make([]string, 0, len(tlds))
	seen := map[string]bool{}
	for _, tld := range tlds {
		normalized, err := NormalizeTLD(tld)
		if err != nil {
			return nil, fmt.Errorf("invalid configured TLD %q: %w", tld, err)
		}
		if !seen[normalized] {
			seen[normalized] = true
			out = append(out, normalized)
		}
	}
	return out, nil
}

// ExpandWords builds domain targets from already-normalized words and TLDs.
func ExpandWords(words []string, tlds []string, wordFirst bool) []types.Target {
	normalized, err := NormalizeTLDs(tlds)
	if err != nil {
		return nil
	}
	return expandWords(words, normalized, wordFirst, map[string]bool{})
}

func expandWords(words []string, tlds []string, wordFirst bool, seen map[string]bool) []types.Target {
	var targets []types.Target
	appendTarget := func(word string, tld string) {
		domain, err := BuildDomainFromWord(word, tld)
		if err != nil || seen[domain] {
			return
		}
		seen[domain] = true
		targets = append(targets, types.Target{Domain: domain, Input: word})
	}

	if wordFirst {
		for _, word := range words {
			for _, tld := range tlds {
				appendTarget(word, tld)
			}
		}
		return targets
	}
	for _, tld := range tlds {
		for _, word := range words {
			appendTarget(word, tld)
		}
	}
	return targets
}

func dedupeWords(words []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(words))
	for _, word := range words {
		if !seen[word] {
			seen[word] = true
			out = append(out, word)
		}
	}
	return out
}

func looksLikeDomainOrURL(value string) bool {
	lower := strings.ToLower(value)
	return strings.Contains(lower, "://") || strings.Contains(value, ".") || strings.ContainsAny(value, "/?#")
}
