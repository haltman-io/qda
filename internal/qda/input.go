package qda

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

func LoadTargets(path string, tlds []string) ([]Target, []SkippedInput, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open input: %w", err)
	}
	defer file.Close()

	normalizedTLDs := make([]string, 0, len(tlds))
	for _, tld := range tlds {
		normalized, err := NormalizeTLD(tld)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid configured TLD %q: %w", tld, err)
		}
		normalizedTLDs = append(normalizedTLDs, normalized)
	}

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	var targets []Target
	var skipped []SkippedInput
	seen := map[string]bool{}
	lineNumber := 0

	for scanner.Scan() {
		lineNumber++
		raw := stripInlineComment(scanner.Text())
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}

		if looksLikeDomainOrURL(value) {
			domain, err := RegistrableDomain(value)
			if err != nil {
				skipped = append(skipped, SkippedInput{Input: value, LineNumber: lineNumber, Reason: err.Error()})
				continue
			}
			if !seen[domain] {
				targets = append(targets, Target{Domain: domain, Input: value, LineNumber: lineNumber})
				seen[domain] = true
			}
			continue
		}

		word, err := NormalizeWord(value)
		if err != nil {
			skipped = append(skipped, SkippedInput{Input: value, LineNumber: lineNumber, Reason: err.Error()})
			continue
		}

		for _, tld := range normalizedTLDs {
			domain, err := BuildDomainFromWord(word, tld)
			if err != nil {
				skipped = append(skipped, SkippedInput{Input: value, LineNumber: lineNumber, Reason: err.Error()})
				continue
			}
			if !seen[domain] {
				targets = append(targets, Target{Domain: domain, Input: value, LineNumber: lineNumber})
				seen[domain] = true
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("read input: %w", err)
	}

	return targets, skipped, nil
}

func stripInlineComment(line string) string {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "#") {
		return ""
	}
	return line
}

func looksLikeDomainOrURL(value string) bool {
	lower := strings.ToLower(value)
	return strings.Contains(lower, "://") || strings.Contains(value, ".") || strings.ContainsAny(value, "/?#")
}
