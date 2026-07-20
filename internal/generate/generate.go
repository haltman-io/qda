// Package generate produces wordlists and domain lists on demand:
// character combinations (like the old gename.bash), wordlist merging and
// word×TLD expansion.
package generate

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"qda/internal/domainx"
)

// Charsets supported by -charset.
var Charsets = map[string][]rune{
	"letters": []rune("abcdefghijklmnopqrstuvwxyz"),
	"digits":  []rune("0123456789"),
	"alnum":   []rune("abcdefghijklmnopqrstuvwxyz0123456789"),
}

// Combinations streams every combination of the given length over the
// charset to w, one per line, without buffering the whole list in memory.
func Combinations(w io.Writer, length int, charset string) (int64, error) {
	chars, ok := Charsets[charset]
	if !ok {
		return 0, fmt.Errorf("unknown charset %q (use letters, digits or alnum)", charset)
	}
	if length < 1 {
		return 0, fmt.Errorf("length must be at least 1")
	}

	writer := bufio.NewWriterSize(w, 64*1024)
	var count int64
	indices := make([]int, length)
	buffer := make([]rune, length)

	for {
		for i, idx := range indices {
			buffer[i] = chars[idx]
		}
		if _, err := writer.WriteString(string(buffer) + "\n"); err != nil {
			return count, err
		}
		count++
		if count%100000 == 0 {
			if err := writer.Flush(); err != nil {
				return count, err
			}
		}

		// Odometer increment.
		position := length - 1
		for position >= 0 {
			indices[position]++
			if indices[position] < len(chars) {
				break
			}
			indices[position] = 0
			position--
		}
		if position < 0 {
			break
		}
	}
	return count, writer.Flush()
}

// Merge reads wordlists, normalizes words, dedupes and writes them in
// sorted order (or input order when sortedOutput is false).
func Merge(w io.Writer, paths []string, sortedOutput bool) (int, error) {
	seen := map[string]bool{}
	var words []string
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			return 0, fmt.Errorf("open %s: %w", path, err)
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			word, err := domainx.NormalizeWord(line)
			if err != nil {
				continue
			}
			if !seen[word] {
				seen[word] = true
				words = append(words, word)
			}
		}
		err = scanner.Err()
		file.Close()
		if err != nil {
			return 0, fmt.Errorf("read %s: %w", path, err)
		}
	}

	if sortedOutput {
		sort.Slice(words, func(i, j int) bool {
			if len(words[i]) != len(words[j]) {
				return len(words[i]) < len(words[j])
			}
			return words[i] < words[j]
		})
	}

	writer := bufio.NewWriter(w)
	for _, word := range words {
		if _, err := writer.WriteString(word + "\n"); err != nil {
			return len(words), err
		}
	}
	return len(words), writer.Flush()
}

// ExpandDomains writes word×TLD combinations as full domains.
func ExpandDomains(w io.Writer, words []string, tlds []string) (int, error) {
	targets := domainx.ExpandWords(words, tlds, true)
	writer := bufio.NewWriter(w)
	for _, target := range targets {
		if _, err := writer.WriteString(target.Domain + "\n"); err != nil {
			return len(targets), err
		}
	}
	return len(targets), writer.Flush()
}

// ReadWordsFile loads normalized words from a file.
func ReadWordsFile(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	var words []string
	seen := map[string]bool{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		word, err := domainx.NormalizeWord(line)
		if err != nil || seen[word] {
			continue
		}
		seen[word] = true
		words = append(words, word)
	}
	return words, scanner.Err()
}
