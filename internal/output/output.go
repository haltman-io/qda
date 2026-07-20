// Package output renders the ProjectDiscovery-style console experience:
// a colored, per-domain streaming line for every check on stdout, and
// leveled operational logs ([INF]/[WRN]/[ERR]/[DBG]) on stderr.
package output

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"qda/internal/types"
)

// Level controls verbosity.
type Level int

const (
	// LevelSilent prints results only (no banner, no logs).
	LevelSilent Level = iota
	// LevelNormal prints results plus operational logs.
	LevelNormal
	// LevelVerbose adds per-source retry/rotation detail.
	LevelVerbose
	// LevelDebug adds HTTP-level detail.
	LevelDebug
)

const (
	ansiReset   = "\x1b[0m"
	ansiBold    = "\x1b[1m"
	ansiGreen   = "\x1b[32;1m"
	ansiYellow  = "\x1b[33;1m"
	ansiRed     = "\x1b[31;1m"
	ansiCyan    = "\x1b[36m"
	ansiMagenta = "\x1b[35;1m"
	ansiGray    = "\x1b[90m"
	ansiMuted   = "\x1b[2;90m"
)

// Printer serializes all console output.
type Printer struct {
	mu      sync.Mutex
	out     io.Writer
	err     io.Writer
	level   Level
	color   bool
	fileOut io.Writer // optional -o file receiving plain result lines
}

// New creates a Printer. color should already account for NO_COLOR and TTY.
func New(out io.Writer, err io.Writer, level Level, color bool) *Printer {
	return &Printer{out: out, err: err, level: level, color: color}
}

// SetFileOutput mirrors plain (uncolored) result lines to a file.
func (p *Printer) SetFileOutput(w io.Writer) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fileOut = w
}

// Level returns the configured verbosity.
func (p *Printer) Level() Level { return p.level }

// Result prints one domain result line immediately.
func (p *Printer) Result(result types.Result, hideRegistered bool) {
	if hideRegistered && (result.Availability == types.AvailabilityRegistered || result.Availability == types.AvailabilityReserved) {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	fmt.Fprintln(p.out, p.colorizeLine(formatResultLine(result)))
	if p.fileOut != nil {
		fmt.Fprintln(p.fileOut, formatResultLinePlain(result))
	}
}

// Infof prints an [INF] log line (suppressed in silent mode).
func (p *Printer) Infof(format string, args ...any) {
	p.logf(LevelNormal, "INF", ansiCyan, format, args...)
}

// Warnf prints a [WRN] log line.
func (p *Printer) Warnf(format string, args ...any) {
	p.logf(LevelNormal, "WRN", ansiYellow, format, args...)
}

// Errorf prints an [ERR] log line.
func (p *Printer) Errorf(format string, args ...any) {
	p.logf(LevelNormal, "ERR", ansiRed, format, args...)
}

// Verbosef prints a [VRB] log line when verbosity is verbose or higher.
func (p *Printer) Verbosef(format string, args ...any) {
	p.logf(LevelVerbose, "VRB", ansiGray, format, args...)
}

// Debugf prints a [DBG] log line when verbosity is debug.
func (p *Printer) Debugf(format string, args ...any) {
	p.logf(LevelDebug, "DBG", ansiMagenta, format, args...)
}

// Raw writes a raw line to stderr (used by the banner).
func (p *Printer) Raw(text string) {
	if p.level == LevelSilent {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	fmt.Fprintln(p.err, text)
}

func (p *Printer) logf(minLevel Level, tag string, color string, format string, args ...any) {
	if p.level < minLevel || p.level == LevelSilent {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	message := fmt.Sprintf(format, args...)
	if p.color {
		fmt.Fprintf(p.err, "%s[%s]%s %s\n", color, tag, ansiReset, message)
	} else {
		fmt.Fprintf(p.err, "[%s] %s\n", tag, message)
	}
}

// colorizeLine applies status colors to a fully formatted line.
func (p *Printer) colorizeLine(line string) string {
	if !p.color {
		return line
	}
	status, rest, ok := strings.Cut(line, "] ")
	if !ok {
		return line
	}
	status = strings.TrimPrefix(status, "[")
	color, bold := statusColor(status)
	coloredStatus := color + "[" + status + "]" + ansiReset
	if bold {
		if idx := strings.Index(rest, " ["); idx > 0 {
			return coloredStatus + " " + ansiBold + rest[:idx] + ansiReset + rest[idx:]
		}
		return coloredStatus + " " + ansiBold + rest + ansiReset
	}
	return coloredStatus + " " + rest
}

func statusColor(status string) (string, bool) {
	switch status {
	case "AVAILABLE", "PREMIUM":
		return ansiGreen, true
	case "SOON":
		return ansiYellow, true
	case "REGISTERED", "RESERVED":
		return ansiMuted, false
	case "RATE-LIMITED":
		return ansiRed, false
	case "UNKNOWN", "INVALID":
		return ansiMagenta, false
	default:
		return ansiGray, false
	}
}

// StatusLabel maps a result to the console label.
func StatusLabel(result types.Result) string {
	if types.IsAvailableLike(result) {
		if result.Availability == types.AvailabilityPremium {
			return "PREMIUM"
		}
		return "AVAILABLE"
	}
	if types.IsAvailableSoon(result) {
		return "SOON"
	}
	switch result.Availability {
	case types.AvailabilityRegistered:
		return "REGISTERED"
	case types.AvailabilityRateLimited:
		return "RATE-LIMITED"
	case types.AvailabilityReserved:
		return "RESERVED"
	case types.AvailabilityInvalid:
		return "INVALID"
	default:
		return "UNKNOWN"
	}
}

func formatResultLine(result types.Result) string {
	return "[" + StatusLabel(result) + "] " + result.Domain + " " + resultAttrs(result)
}

func formatResultLinePlain(result types.Result) string {
	return formatResultLine(result)
}

func resultAttrs(result types.Result) string {
	var attrs []string
	attrs = append(attrs, sourceLabel(result))
	if result.Lifecycle != "" && result.Lifecycle != "available" && result.Lifecycle != "active" {
		attrs = append(attrs, result.Lifecycle)
	}
	if result.ExpiresAt != "" {
		attrs = append(attrs, "expires:"+shortDate(result.ExpiresAt))
	}
	if result.ExpiresInDays != nil && result.ExpiringSoon {
		attrs = append(attrs, "in:"+strconv.Itoa(*result.ExpiresInDays)+"d")
	}
	if result.Registrar != "" {
		attrs = append(attrs, "registrar:"+result.Registrar)
	}
	if result.Price != nil && result.Price.RegistrationCost != "" {
		price := result.Price.RegistrationCost
		if result.Price.Currency != "" {
			price += " " + result.Price.Currency
		}
		attrs = append(attrs, "price:"+price)
	}
	if result.CacheHit {
		attrs = append(attrs, "cached")
	}
	if result.Error != "" {
		attrs = append(attrs, "err:"+truncate(result.Error, 120))
	}
	return "[" + strings.Join(attrs, "] [") + "]"
}

func sourceLabel(result types.Result) string {
	if result.CacheHit {
		return "cache"
	}
	if len(result.Sources) > 0 {
		last := result.Sources[len(result.Sources)-1]
		if last.Name != "" {
			return last.Name
		}
	}
	source := result.Source
	switch {
	case strings.Contains(source, "cloudflare.com"):
		return "cloudflare"
	case strings.Contains(source, "hostinger.com"):
		return "hostinger"
	case strings.Contains(source, "vercel.com"):
		return "vercel"
	case source != "":
		return "rdap"
	default:
		return "-"
	}
}

func shortDate(value string) string {
	if len(value) >= 10 {
		return value[:10]
	}
	return value
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max-1] + "…"
}

// CountResults tallies availability values.
func CountResults(results []types.Result) map[types.Availability]int {
	counts := map[types.Availability]int{}
	for _, result := range results {
		counts[result.Availability]++
	}
	return counts
}

// Summary renders the final scan summary on stderr.
func (p *Printer) Summary(results []types.Result, elapsed time.Duration, requeued int) {
	if p.level == LevelSilent {
		return
	}
	counts := CountResults(results)
	cacheHits := 0
	var interesting []types.Result
	for _, result := range results {
		if result.CacheHit {
			cacheHits++
		}
		if types.IsAvailableLike(result) || types.IsAvailableSoon(result) {
			interesting = append(interesting, result)
		}
	}

	rate := 0.0
	if elapsed > 0 {
		rate = float64(len(results)) / elapsed.Seconds()
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	w := p.err
	fmt.Fprintln(w)
	p.printfLocked(w, ansiBold, "─── SCAN SUMMARY ───────────────────────────────\n")
	fmt.Fprintf(w, "  duration:      %s (%.1f checks/s)\n", elapsed.Round(time.Second), rate)
	fmt.Fprintf(w, "  checked:       %d", len(results))
	if cacheHits > 0 {
		fmt.Fprintf(w, " (%d from cache)", cacheHits)
	}
	fmt.Fprintln(w)
	if requeued > 0 {
		fmt.Fprintf(w, "  requeued:      %d\n", requeued)
	}
	fmt.Fprintf(w, "  available:     %d\n", counts[types.AvailabilityAvailable])
	fmt.Fprintf(w, "  premium:       %d\n", counts[types.AvailabilityPremium])
	fmt.Fprintf(w, "  soon:          %d\n", countSoon(results))
	fmt.Fprintf(w, "  registered:    %d\n", counts[types.AvailabilityRegistered])
	fmt.Fprintf(w, "  reserved:      %d\n", counts[types.AvailabilityReserved])
	fmt.Fprintf(w, "  rate_limited:  %d\n", counts[types.AvailabilityRateLimited])
	fmt.Fprintf(w, "  unknown:       %d\n", counts[types.AvailabilityUnknown])

	if len(interesting) > 0 {
		fmt.Fprintln(w)
		p.printfLocked(w, ansiGreen, "─── INTERESTING DOMAINS ────────────────────────\n")
		const maxInteresting = 50
		shown := interesting
		if len(shown) > maxInteresting {
			shown = shown[:maxInteresting]
		}
		for _, result := range shown {
			line := fmt.Sprintf("  %-10s %s", "["+StatusLabel(result)+"]", result.Domain)
			if result.ExpiresAt != "" {
				line += " (expires " + shortDate(result.ExpiresAt) + ")"
			}
			if p.color {
				color, _ := statusColor(StatusLabel(result))
				fmt.Fprintf(w, "%s%s%s\n", color, line, ansiReset)
			} else {
				fmt.Fprintln(w, line)
			}
		}
		if len(interesting) > maxInteresting {
			fmt.Fprintf(w, "  ... and %d more (see exports / qda db -available)\n", len(interesting)-maxInteresting)
		}
	}
	fmt.Fprintln(w)
}

func (p *Printer) printfLocked(w io.Writer, color string, format string, args ...any) {
	if p.color {
		fmt.Fprintf(w, "%s%s%s", color, fmt.Sprintf(format, args...), ansiReset)
		return
	}
	fmt.Fprintf(w, format, args...)
}

func countSoon(results []types.Result) int {
	count := 0
	for _, result := range results {
		if types.IsAvailableSoon(result) && !types.IsAvailableLike(result) {
			count++
		}
	}
	return count
}

// IsTerminal reports whether the file descriptor is a terminal.
func IsTerminal(file *os.File) bool {
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// ColorsEnabled decides color usage from flags and environment.
func ColorsEnabled(noColorFlag bool, out *os.File) bool {
	if noColorFlag {
		return false
	}
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	return IsTerminal(out)
}
