package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"qda/internal/config"
	"qda/internal/output"
	"qda/internal/store"
	"qda/internal/types"
)

func dbCmd(args []string, stdout io.Writer, stderr io.Writer) int {
	settings := config.Default()

	fs := flag.NewFlagSet("qda db", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var configPath, status, tld, query, outputJSON, dbPath string
	var available, expiring, showStats bool
	var expiringIn int
	var limit int
	fs.StringVar(&configPath, "config", "", "Path to a TOML configuration file (default: ./qda.toml when present)")
	fs.StringVar(&dbPath, "db", "", "Override the database path")
	fs.StringVar(&status, "status", "", "Filter by availability (available, registered, reserved, premium, rate_limited, unknown)")
	fs.BoolVar(&available, "available", false, "Show only available/premium/soon domains")
	fs.BoolVar(&expiring, "expiring", false, "Show only domains flagged expiring soon")
	fs.IntVar(&expiringIn, "expiring-in", -1, "Show domains expiring within N days")
	fs.StringVar(&tld, "tld", "", "Filter by TLD (e.g. net, lat, com.br)")
	fs.StringVar(&query, "q", "", "Filter domains containing this substring")
	fs.IntVar(&limit, "limit", 0, "Maximum records to show (0 = all)")
	fs.BoolVar(&showStats, "stats", false, "Show database statistics")
	fs.StringVar(&outputJSON, "json", "", "Write matching records as JSON to this path")
	fs.Usage = func() {
		fmt.Fprintf(stderr, `Query the local qda database.

USAGE
  qda db                     Show database statistics
  qda db -available          List available/premium/soon domains
  qda db -status registered -tld net
  qda db -expiring-in 30     Domains expiring within 30 days
  qda db -q kernel -json out.json

OPTIONS
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if configPath == "" && fileExists("qda.toml") {
		configPath = "qda.toml"
	}
	if configPath != "" {
		if err := config.LoadFile(configPath, &settings); err != nil {
			fmt.Fprintf(stderr, "[ERR] %v\n", err)
			return 1
		}
	}
	if dbPath != "" {
		settings.Store.Path = dbPath
	}

	db, err := store.Open(settings.Store.Path, settings.Store.Enabled, settings.Store.RegisteredTTL, settings.Store.ReservedTTL)
	if err != nil {
		fmt.Fprintf(stderr, "[ERR] %v\n", err)
		return 1
	}
	if db.Warning() != "" {
		fmt.Fprintf(stderr, "[WRN] %s\n", db.Warning())
	}

	// Default view: statistics.
	if showStats || (status == "" && !available && !expiring && expiringIn < 0 && tld == "" && query == "" && outputJSON == "") {
		printDBStats(stdout, db)
		return 0
	}

	q := store.Query{Status: status, TLD: tld, Contains: query, AvailableSoon: available}
	if expiringIn >= 0 {
		q.ExpiringIn = &expiringIn
	}
	records := db.Query(q)
	if expiring {
		var filtered []*store.Record
		for _, record := range records {
			if record.ExpiringSoon {
				filtered = append(filtered, record)
			}
		}
		records = filtered
	}
	total := len(records)
	if limit > 0 && len(records) > limit {
		records = records[:limit]
	}

	if outputJSON != "" {
		data, err := json.MarshalIndent(records, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "[ERR] %v\n", err)
			return 1
		}
		if err := os.WriteFile(outputJSON, data, 0o644); err != nil {
			fmt.Fprintf(stderr, "[ERR] write %s: %v\n", outputJSON, err)
			return 1
		}
		fmt.Fprintf(stdout, "Wrote %d records to %s\n", len(records), outputJSON)
		return 0
	}

	stdoutFile, _ := stdout.(*os.File)
	printer := output.New(stdout, stderr, output.LevelNormal, output.ColorsEnabled(false, stdoutFile))
	for _, record := range records {
		printer.Result(recordToResult(record), false)
	}
	fmt.Fprintf(stdout, "\n%d record(s)", len(records))
	if limit > 0 && total > len(records) {
		fmt.Fprintf(stdout, " (of %d matching; increase -limit to see all)", total)
	}
	fmt.Fprintln(stdout)
	return 0
}

func printDBStats(out io.Writer, db *store.Store) {
	stats := db.Stats()
	fmt.Fprintf(out, "qda local database: %s\n", db.Path())
	fmt.Fprintf(out, "  total domains:   %d\n", stats.Total)
	fmt.Fprintf(out, "  available:       %d\n", stats.Available)
	fmt.Fprintf(out, "  available soon:  %d\n", stats.AvailableSoon)
	fmt.Fprintf(out, "  expiring soon:   %d\n", stats.ExpiringSoon)
	if !stats.LastCheckedAt.IsZero() {
		fmt.Fprintf(out, "  last check:      %s\n", stats.LastCheckedAt.Format("2006-01-02 15:04:05 UTC"))
	}
	if len(stats.ByAvailability) > 0 {
		fmt.Fprintln(out, "  by availability:")
		order := []string{"available", "premium", "registered", "reserved", "pending_delete", "redemption", "rate_limited", "unknown", "invalid"}
		printed := map[string]bool{}
		for _, key := range order {
			if count, ok := stats.ByAvailability[key]; ok {
				fmt.Fprintf(out, "    %-14s %d\n", key+":", count)
				printed[key] = true
			}
		}
		for key, count := range stats.ByAvailability {
			if !printed[key] {
				fmt.Fprintf(out, "    %-14s %d\n", key+":", count)
			}
		}
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Use -available, -status, -expiring-in, -tld, -q to list records.")
}

func recordToResult(record *store.Record) types.Result {
	result := types.Result{
		Domain:        record.Domain,
		Availability:  record.Availability,
		Lifecycle:     record.Lifecycle,
		Confidence:    record.Confidence,
		CreatedAt:     record.CreatedAt,
		ExpiresAt:     record.ExpiresAt,
		UpdatedAt:     record.UpdatedAt,
		Statuses:      record.Statuses,
		Registrar:     record.Registrar,
		Source:        record.Source,
		Error:         record.Error,
		Price:         record.Price,
		CheckedAt:     record.LastCheckedAt,
		ExpiringSoon:  record.ExpiringSoon,
		ExpiresInDays: record.ExpiresInDays,
	}
	if result.Lifecycle == "" {
		result.Lifecycle, result.ExpiringSoon, result.ExpiresInDays = types.DetermineLifecycle(
			record.Availability, record.Statuses, record.ExpiresAt, 30, record.LastCheckedAt)
	}
	return result
}

