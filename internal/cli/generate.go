package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"qda/internal/generate"
)

func generateCmd(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("qda generate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var length, minLen, maxLen int
	var charset, tldsRaw, mergeRaw, outputPath string
	var sortOutput bool
	fs.IntVar(&length, "len", 0, "Combination length (e.g. 3)")
	fs.IntVar(&minLen, "min", 0, "Minimum length for ranges (use with -max)")
	fs.IntVar(&maxLen, "max", 0, "Maximum length for ranges (use with -min)")
	fs.StringVar(&charset, "charset", "alnum", "Character set: letters, digits, alnum")
	fs.StringVar(&tldsRaw, "tlds", "", "Also expand generated words into domains with these TLDs (comma-separated)")
	fs.StringVar(&mergeRaw, "merge", "", "Merge/dedupe these wordlist files (comma-separated)")
	fs.BoolVar(&sortOutput, "sort", true, "Sort merged wordlists by length, then alphabetically")
	fs.StringVar(&outputPath, "o", "", "Output file (default: stdout)")
	fs.Usage = func() {
		fmt.Fprintf(stderr, `Generate wordlists and domain lists on demand.

USAGE
  qda generate -len 3 -charset alnum -o combinacoes_3.txt
  qda generate -min 1 -max 2 -charset letters -o shorts.txt
  qda generate -merge wl-kalitools-clean.txt,wl-parrot-tools-clean.txt -o merged.txt
  qda generate -merge words.txt -tlds net,org,com -o domains.txt

OPTIONS
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	var out io.Writer = stdout
	var file *os.File
	if outputPath != "" {
		var err error
		file, err = os.Create(outputPath)
		if err != nil {
			fmt.Fprintf(stderr, "[ERR] create %s: %v\n", outputPath, err)
			return 1
		}
		defer file.Close()
		out = file
	}

	switch {
	case mergeRaw != "":
		paths := splitCSV(mergeRaw)
		count, err := generate.Merge(out, paths, sortOutput)
		if err != nil {
			fmt.Fprintf(stderr, "[ERR] %v\n", err)
			return 1
		}
		fmt.Fprintf(stderr, "[INF] merged %d unique words from %d file(s)\n", count, len(paths))
		if tldsRaw != "" {
			return expandFromWords(paths, tldsRaw, outputPath, stderr)
		}

	case length > 0 || (minLen > 0 && maxLen > 0):
		from, to := length, length
		if minLen > 0 {
			from, to = minLen, maxLen
		}
		if from < 1 || to < from {
			fmt.Fprintln(stderr, "[ERR] invalid length range")
			return 2
		}
		var total int64
		for current := from; current <= to; current++ {
			count, err := generate.Combinations(out, current, charset)
			if err != nil {
				fmt.Fprintf(stderr, "[ERR] %v\n", err)
				return 1
			}
			total += count
		}
		fmt.Fprintf(stderr, "[INF] generated %d combination(s)\n", total)

	default:
		fs.Usage()
		return 2
	}
	return 0
}

func expandFromWords(paths []string, tldsRaw string, outputPath string, stderr io.Writer) int {
	tlds := splitCSV(tldsRaw)
	var words []string
	for _, path := range paths {
		loaded, err := generate.ReadWordsFile(path)
		if err != nil {
			fmt.Fprintf(stderr, "[ERR] %v\n", err)
			return 1
		}
		words = append(words, loaded...)
	}

	var out io.Writer = os.Stdout
	if outputPath != "" {
		file, err := os.Create(outputPath)
		if err != nil {
			fmt.Fprintf(stderr, "[ERR] create %s: %v\n", outputPath, err)
			return 1
		}
		defer file.Close()
		out = file
	}
	count, err := generate.ExpandDomains(out, words, tlds)
	if err != nil {
		fmt.Fprintf(stderr, "[ERR] %v\n", err)
		return 1
	}
	fmt.Fprintf(stderr, "[INF] expanded %d words into %d domains\n", len(words), count)
	return 0
}

func splitCSV(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
