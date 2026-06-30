package qda

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func Execute(ctx context.Context, settings Settings, stdout io.Writer, stderr io.Writer) error {
	targets, skipped, err := LoadTargets(settings.InputPath, settings.TLDs)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return fmt.Errorf("no valid domains to check")
	}

	var results []Result
	if ShouldUseTUI(settings, stdout) {
		results, err = RunInteractive(ctx, settings, targets, skipped)
	} else {
		fmt.Fprintf(stdout, "Loaded %d domains", len(targets))
		if len(skipped) > 0 {
			fmt.Fprintf(stdout, " and skipped %d invalid input lines", len(skipped))
		}
		fmt.Fprintln(stdout, ".")
		results, err = RunPlain(ctx, settings, targets, stdout)
	}

	var warning WarningError
	if err != nil {
		if w, ok := err.(WarningError); ok {
			warning = w
			err = nil
		} else {
			return err
		}
	}

	if settings.IncludeInvalid {
		for _, item := range skipped {
			results = append(results, InvalidResult(item))
		}
		SortResults(results)
	}

	if settings.CSVOutput != "" {
		if err := WriteCSV(settings.CSVOutput, results); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Wrote CSV results to %s\n", settings.CSVOutput)
	}
	if settings.JSONOutput != "" {
		if err := WriteJSON(settings.JSONOutput, results); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Wrote JSON results to %s\n", settings.JSONOutput)
	}

	if err := SendNotifications(ctx, settings, results); err != nil {
		return err
	}

	PrintSummaryWithSettings(stdout, results, settings)
	if warning != "" {
		fmt.Fprintln(stderr, "Warning:", warning)
	}
	return nil
}

func RunPlain(ctx context.Context, settings Settings, targets []Target, stdout io.Writer) ([]Result, error) {
	resultCh, errCh := RunChecks(ctx, settings, targets)
	var results []Result
	for result := range resultCh {
		results = append(results, result)
		if shouldPrintConsoleResult(settings, result) {
			fmt.Fprintln(stdout, FormatPlainResult(result))
		}
	}
	err := <-errCh
	SortResults(results)
	return results, err
}

func FormatPlainResult(result Result) string {
	expires := result.ExpiresAt
	if expires == "" {
		expires = "-"
	}
	cfReason := ""
	if result.CloudflareReason != "" {
		cfReason = " reason=" + result.CloudflareReason
	}
	errText := ""
	if result.Error != "" {
		errText = " | " + result.Error
	}
	label := colorizeResult(result, fmt.Sprintf("%-18s", resultLogLabel(result)))
	domain := colorizeDomain(result, fmt.Sprintf("%-36s", result.Domain))
	return fmt.Sprintf("%s %s lifecycle=%-24s source=%-12s expires=%s%s%s",
		label,
		domain,
		result.Lifecycle,
		resultSourceLabel(result),
		expires,
		cfReason,
		errText,
	)
}

func WriteCSV(path string, results []Result) error {
	if err := ensureParentDir(path); err != nil {
		return err
	}
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create CSV: %w", err)
	}
	defer file.Close()

	writer := csv.NewWriter(file)

	header := []string{
		"domain",
		"availability",
		"lifecycle",
		"confidence",
		"created_at",
		"expires_at",
		"updated_at",
		"rdap_updated_at",
		"expiring_soon",
		"expires_in_days",
		"cache_hit",
		"cache_reason",
		"cloudflare_registrable",
		"cloudflare_reason",
		"cloudflare_tier",
		"cloudflare_currency",
		"cloudflare_registration_cost",
		"cloudflare_renewal_cost",
		"vercel_available",
		"vercel_registration_cost",
		"vercel_renewal_cost",
		"vercel_transfer_cost",
		"vercel_price_years",
		"hostinger_available",
		"hostinger_restriction",
		"statuses",
		"registrar",
		"source",
		"http_status",
		"error",
		"input",
		"line_number",
		"checked_at",
	}
	if err := writer.Write(header); err != nil {
		return fmt.Errorf("write CSV header: %w", err)
	}

	for _, result := range results {
		expiresInDays := ""
		if result.ExpiresInDays != nil {
			expiresInDays = strconv.Itoa(*result.ExpiresInDays)
		}
		cloudflareRegistrable := ""
		if result.CloudflareRegistrable != nil {
			cloudflareRegistrable = strconv.FormatBool(*result.CloudflareRegistrable)
		}
		var pricing Pricing
		if result.CloudflarePricing != nil {
			pricing = *result.CloudflarePricing
		}
		vercelAvailable := ""
		if result.VercelAvailable != nil {
			vercelAvailable = strconv.FormatBool(*result.VercelAvailable)
		}
		var vercelPricing Pricing
		if result.VercelPricing != nil {
			vercelPricing = *result.VercelPricing
		}
		hostingerAvailable := ""
		if result.HostingerAvailable != nil {
			hostingerAvailable = strconv.FormatBool(*result.HostingerAvailable)
		}
		record := []string{
			result.Domain,
			string(result.Availability),
			result.Lifecycle,
			result.Confidence,
			result.CreatedAt,
			result.ExpiresAt,
			result.UpdatedAt,
			result.RDAPUpdatedAt,
			strconv.FormatBool(result.ExpiringSoon),
			expiresInDays,
			strconv.FormatBool(result.CacheHit),
			result.CacheReason,
			cloudflareRegistrable,
			result.CloudflareReason,
			result.CloudflareTier,
			pricing.Currency,
			pricing.RegistrationCost,
			pricing.RenewalCost,
			vercelAvailable,
			vercelPricing.RegistrationCost,
			vercelPricing.RenewalCost,
			vercelPricing.TransferCost,
			vercelPricing.Years,
			hostingerAvailable,
			result.HostingerRestriction,
			strings.Join(result.Statuses, ";"),
			result.Registrar,
			result.Source,
			strconv.Itoa(result.HTTPStatus),
			result.Error,
			result.Input,
			strconv.Itoa(result.LineNumber),
			result.CheckedAt.Format(time.RFC3339),
		}
		if err := writer.Write(record); err != nil {
			return fmt.Errorf("write CSV row: %w", err)
		}
	}
	writer.Flush()
	return writer.Error()
}

func WriteJSON(path string, results []Result) error {
	if err := ensureParentDir(path); err != nil {
		return err
	}
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create JSON: %w", err)
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(results)
}

func ensureParentDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "." || dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	return nil
}

func CountResults(results []Result) map[Availability]int {
	counts := map[Availability]int{}
	for _, result := range results {
		counts[result.Availability]++
	}
	return counts
}

func PrintSummary(out io.Writer, results []Result) {
	PrintSummaryWithSettings(out, results, Settings{})
}

func PrintSummaryWithSettings(out io.Writer, results []Result, settings Settings) {
	counts := CountResults(results)
	cacheHits := 0
	for _, result := range results {
		if result.CacheHit {
			cacheHits++
		}
	}
	fmt.Fprintf(out, "\nSummary: %d total | %d available | %d registered | %d reserved | %d premium | %d pending_delete | %d redemption | %d rate_limited | %d unknown | %d cache_hits\n",
		len(results),
		counts[AvailabilityAvailable],
		counts[AvailabilityRegistered],
		counts[AvailabilityReserved],
		counts[AvailabilityPremium],
		counts[AvailabilityPendingDelete],
		counts[AvailabilityRedemption],
		counts[AvailabilityRateLimited],
		counts[AvailabilityUnknown],
		cacheHits,
	)
	PrintFinalResultsTableWithSettings(out, results, settings)
}

func PrintFinalResultsTable(out io.Writer, results []Result) {
	PrintFinalResultsTableWithSettings(out, results, Settings{})
}

func PrintFinalResultsTableWithSettings(out io.Writer, results []Result, settings Settings) {
	sorted := append([]Result(nil), results...)
	SortResults(sorted)

	fmt.Fprintln(out)
	fmt.Fprintln(out, ansiBold+"FINAL RESULTS"+ansiReset)
	fmt.Fprintf(out, "%-18s %-38s %-24s %-22s %-12s %s\n", "STATUS", "DOMAIN", "LIFECYCLE", "EXPIRES", "SOURCE", "DETAIL")
	fmt.Fprintf(out, "%-18s %-38s %-24s %-22s %-12s %s\n", strings.Repeat("-", 18), strings.Repeat("-", 38), strings.Repeat("-", 24), strings.Repeat("-", 22), strings.Repeat("-", 12), strings.Repeat("-", 24))

	for _, result := range sorted {
		if !shouldPrintConsoleResult(settings, result) {
			continue
		}
		expires := result.ExpiresAt
		if expires == "" {
			expires = "-"
		}
		detail := resultDetail(result)
		status := colorizeResult(result, fmt.Sprintf("%-18s", resultLogLabel(result)))
		domain := colorizeDomain(result, fmt.Sprintf("%-38s", result.Domain))
		fmt.Fprintf(out, "%s %s %-24s %-22s %-12s %s\n",
			status,
			domain,
			result.Lifecycle,
			expires,
			resultSourceLabel(result),
			detail,
		)
	}
}

func shouldPrintConsoleResult(settings Settings, result Result) bool {
	if !settings.HideRegisteredReserved {
		return true
	}
	return result.Availability != AvailabilityRegistered && result.Availability != AvailabilityReserved
}

func resultDetail(result Result) string {
	var parts []string
	if result.CloudflareReason != "" {
		parts = append(parts, "reason="+result.CloudflareReason)
	}
	if result.CloudflareTier != "" {
		parts = append(parts, "tier="+result.CloudflareTier)
	}
	if result.CloudflarePricing != nil {
		price := result.CloudflarePricing.RegistrationCost
		if price != "" {
			parts = append(parts, "price="+price+" "+result.CloudflarePricing.Currency)
		}
	}
	if result.VercelAvailable != nil {
		parts = append(parts, "vercel_available="+strconv.FormatBool(*result.VercelAvailable))
	}
	if result.VercelPricing != nil {
		if result.VercelPricing.RegistrationCost != "" {
			parts = append(parts, "vercel_purchase="+result.VercelPricing.RegistrationCost)
		}
		if result.VercelPricing.RenewalCost != "" {
			parts = append(parts, "vercel_renewal="+result.VercelPricing.RenewalCost)
		}
		if result.VercelPricing.TransferCost != "" {
			parts = append(parts, "vercel_transfer="+result.VercelPricing.TransferCost)
		}
	}
	if result.HostingerRestriction != "" {
		parts = append(parts, "hostinger_restriction="+result.HostingerRestriction)
	}
	if result.CacheHit {
		parts = append(parts, "cache_hit=true")
	}
	if result.Error != "" {
		parts = append(parts, "error="+result.Error)
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " ")
}

func resultLogLabel(result Result) string {
	if IsAvailableLike(result) {
		if result.Availability == AvailabilityPremium {
			return "AVAILABLE PREMIUM"
		}
		return "AVAILABLE"
	}
	if IsAvailableSoon(result) {
		return "AVAILABLE SOON"
	}
	switch result.Availability {
	case AvailabilityRegistered:
		return "REGISTERED"
	case AvailabilityRateLimited:
		return "RATE LIMITED"
	case AvailabilityReserved:
		return "RESERVED"
	case AvailabilityInvalid:
		return "INVALID"
	default:
		return "UNKNOWN"
	}
}

func resultSourceLabel(result Result) string {
	if result.CacheHit {
		return "cache"
	}
	if result.Confidence == "rdap_precheck" {
		return "rdap"
	}
	if result.Confidence == "hostinger_authoritative" {
		return "hostinger"
	}
	if result.Confidence == "vercel_authoritative" {
		return "vercel"
	}
	if result.Source != "" {
		return shortSource(result.Source)
	}
	return "-"
}

func shortSource(source string) string {
	if strings.Contains(source, "cloudflare.com") || strings.Contains(source, "/registrar/domain-check") {
		return "cloudflare"
	}
	if strings.Contains(source, "hostinger.com") || strings.Contains(source, "/api/domains/v1/availability") {
		return "hostinger"
	}
	if strings.Contains(source, "vercel.com") || strings.Contains(source, "/v1/registrar/domains/availability") {
		return "vercel"
	}
	if strings.Contains(source, "rdap") {
		return "rdap"
	}
	return source
}

func colorizeResult(result Result, value string) string {
	return resultColor(result) + value + ansiReset
}

func colorizeDomain(result Result, value string) string {
	if IsAvailableLike(result) || IsAvailableSoon(result) {
		return resultColor(result) + ansiBold + value + ansiReset
	}
	return resultColor(result) + value + ansiReset
}

func resultColor(result Result) string {
	if IsAvailableLike(result) {
		return ansiGreen
	}
	if IsAvailableSoon(result) {
		return ansiYellow
	}
	switch result.Availability {
	case AvailabilityRegistered, AvailabilityReserved:
		return ansiMutedGray
	case AvailabilityRateLimited:
		return ansiRed
	case AvailabilityUnknown, AvailabilityInvalid:
		return ansiMagenta
	default:
		return ansiGray
	}
}

const (
	ansiReset     = "\x1b[0m"
	ansiBold      = "\x1b[1m"
	ansiGreen     = "\x1b[32;1m"
	ansiYellow    = "\x1b[33;1m"
	ansiRed       = "\x1b[31;1m"
	ansiBlue      = "\x1b[34m"
	ansiMagenta   = "\x1b[35;1m"
	ansiGray      = "\x1b[90m"
	ansiMutedGray = "\x1b[2;90m"
)
