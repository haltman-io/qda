package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"qda/internal/banner"
	"qda/internal/config"
	"qda/internal/domainx"
	"qda/internal/notify"
	"qda/internal/output"
	"qda/internal/resume"
	"qda/internal/runner"
	"qda/internal/store"
	"qda/internal/types"
	"qda/internal/version"
)

type runCLIFlags struct {
	configPath  string
	inputPath   string
	domains     listFlag
	tlds        listFlag
	tldGroups   listFlag
	proxies     listFlag
	outputPath  string
	silent      bool
	verbose     bool
	debug       bool
	noColor     bool
	jsonl       bool
	resumeScan  bool
	noState     bool
	noNotify    bool
	noProgress  bool
	fallback    bool
	help        bool
}

func runCmd(args []string, stdout io.Writer, stderr io.Writer) int {
	settings := config.Default()
	var cli runCLIFlags

	fs := flag.NewFlagSet("qda run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	bindRunFlags(fs, &settings, &cli)
	fs.Usage = func() { printRunHelp(stderr, fs) }

	normalized, err := normalizeArgs(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if err := fs.Parse(normalized); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if cli.help {
		printRunHelp(stderr, fs)
		return 0
	}

	// Snapshot CLI-provided values, load the config file, then re-apply the
	// flags that were explicitly set on the command line.
	cliValues := settings
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	configPath := cli.configPath
	if configPath == "" {
		if fileExists("qda.toml") {
			configPath = "qda.toml"
		}
	}
	if configPath != "" {
		if err := config.LoadFile(configPath, &settings); err != nil {
			fmt.Fprintf(stderr, "[ERR] %v\n", err)
			return 1
		}
	}
	applyRunOverrides(&settings, &cliValues, &cli, set)

	if err := resolveTLDs(&settings, &cli, set); err != nil {
		fmt.Fprintf(stderr, "[ERR] %v\n", err)
		return 1
	}
	if err := config.Validate(settings); err != nil {
		fmt.Fprintf(stderr, "[ERR] %v\n", err)
		return 1
	}

	// Console setup.
	level := output.LevelNormal
	if cli.verbose {
		level = output.LevelVerbose
	}
	if cli.debug {
		level = output.LevelDebug
	}
	if cli.silent {
		level = output.LevelSilent
	}
	stdoutFile, _ := stdout.(*os.File)
	colors := output.ColorsEnabled(cli.noColor, stdoutFile)
	printer := output.New(stdout, stderr, level, colors)
	if !cli.silent {
		printer.Raw(banner.Text(colors))
		printer.Infof("current qda version v%s", qdaVersion())
		if configPath != "" {
			printer.Infof("using config file: %s", configPath)
		}
	}

	// Targets.
	targets, skipped, resumed, err := resolveTargets(settings, &cli, fs.Args(), stdout, printer)
	if err != nil {
		fmt.Fprintf(stderr, "[ERR] %v\n", err)
		return 1
	}
	if len(targets) == 0 && !cli.resumeScan {
		fmt.Fprintln(stderr, "[ERR] no valid domains to check")
		return 1
	}
	if len(skipped) > 0 {
		printer.Warnf("skipped %d invalid input lines (use include_invalid to export them)", len(skipped))
	}

	// Local database.
	db, err := store.Open(settings.Store.Path, settings.Store.Enabled, settings.Store.RegisteredTTL, settings.Store.ReservedTTL)
	if err != nil {
		fmt.Fprintf(stderr, "[ERR] %v\n", err)
		return 1
	}
	if db.Warning() != "" {
		printer.Warnf("%s", db.Warning())
	}
	if settings.Store.Enabled {
		printer.Debugf("local database: %s (%d records)", db.Path(), db.Len())
	}

	// Resume state.
	resumeManager, resumed, err := setupResume(settings, &cli, targets, resumed, printer)
	if err != nil {
		fmt.Fprintf(stderr, "[ERR] %v\n", err)
		return 1
	}
	if resumed && resumeManager != nil {
		state := resumeManager.State()
		targets = filterTargets(targets, state.Pending)
		if len(targets) == 0 {
			printer.Infof("previous scan has no pending domains; nothing to resume")
			return 0
		}
		printer.Infof("resumed state: %d/%d completed, %d pending", state.Completed, state.Total, len(targets))
	}

	// Engine.
	engine, err := runner.NewEngine(settings, db, printer)
	if err != nil {
		fmt.Fprintf(stderr, "[ERR] %v\n", err)
		return 1
	}
	if err := engine.ValidateReadiness(); err != nil {
		fmt.Fprintf(stderr, "[ERR] %v\n", err)
		return 1
	}
	if engine.FallbackEmpty() {
		printer.Warnf("registrar fallback requested but no credentials configured; running RDAP-only")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	prepareCtx, prepareCancel := context.WithTimeout(ctx, 60*time.Second)
	if err := engine.Prepare(prepareCtx); err != nil {
		prepareCancel()
		fmt.Fprintf(stderr, "[ERR] %v\n", err)
		return 1
	}
	prepareCancel()

	runner.PrintStartInfo(printer, settings, len(targets), resumed)
	runner.Autosave(ctx, db, resumeManager, 15*time.Second, printer)

	// Output sinks.
	var outputFile *os.File
	if cli.outputPath != "" {
		outputFile, err = os.Create(cli.outputPath)
		if err != nil {
			fmt.Fprintf(stderr, "[ERR] create output file: %v\n", err)
			return 1
		}
		defer outputFile.Close()
		printer.SetFileOutput(outputFile)
	}

	run := runner.NewRunner(engine, db, printer, resumeManager)
	run.HideRegistered = settings.HideRegistered
	if cli.jsonl {
		run.JSONL = output.NewJSONLWriter(stdout)
	}

	notifyAvailable := settings.Notify.OnAvailable && !cli.noNotify && notify.HasChannels(settings)
	var notifyMu sync.Mutex
	if notifyAvailable {
		run.OnResult = func(result types.Result) {
			if !types.IsAvailableLike(result) || result.CacheHit {
				return
			}
			notifyMu.Lock()
			defer notifyMu.Unlock()
			if err := notify.Send(context.Background(), settings, notify.AvailablePayload(result)); err != nil {
				printer.Warnf("available notification failed: %v", err)
			}
		}
	}

	started := time.Now()
	results, runErr := run.Run(ctx, targets)
	elapsed := time.Since(started)

	// Final persistence.
	runner.SaveStore(db, printer)
	if resumeManager != nil {
		if errors.Is(runErr, runner.ErrInterrupted) {
			// State was already saved by the runner with the exact pending list.
		} else if !cli.noState {
			resumeManager.MarkFinished()
			if err := resumeManager.Save(); err != nil {
				printer.Warnf("could not finalize resume state: %v", err)
			}
		}
	}

	if errors.Is(runErr, runner.ErrInterrupted) {
		printer.Warnf("scan interrupted: %d/%d completed", len(results), len(targets))
		printer.Infof("resume later with: qda run -resume")
		return 130
	}
	if runErr != nil {
		fmt.Fprintf(stderr, "[ERR] %v\n", runErr)
		return 1
	}

	if settings.IncludeInvalid {
		for _, item := range skipped {
			results = append(results, types.InvalidResult(item))
		}
	}
	sortResults(results)

	// Exports.
	if settings.Export.CSV != "" {
		if err := output.WriteCSV(settings.Export.CSV, results); err != nil {
			printer.Warnf("could not write CSV export: %v", err)
		} else {
			printer.Infof("csv export written to %s", settings.Export.CSV)
		}
	}
	if settings.Export.JSON != "" {
		if err := output.WriteJSON(settings.Export.JSON, results); err != nil {
			printer.Warnf("could not write JSON export: %v", err)
		} else {
			printer.Infof("json export written to %s", settings.Export.JSON)
		}
	}

	printer.Summary(results, elapsed, int(run.Stats().Requeued.Load()))

	// Finish notification.
	if settings.Notify.OnFinish && !cli.noNotify && notify.HasChannels(settings) {
		if err := notify.Send(context.Background(), settings, notify.FinishPayload(results, elapsed)); err != nil {
			printer.Warnf("finish notification failed: %v", err)
		} else {
			printer.Infof("finish notification sent")
		}
	}

	return 0
}

func bindRunFlags(fs *flag.FlagSet, settings *config.Settings, cli *runCLIFlags) {
	fs.StringVar(&cli.configPath, "config", "", "Path to a TOML configuration file (default: ./qda.toml when present)")
	fs.StringVar(&cli.inputPath, "l", "", "File with words/domains (one per line); omit to read stdin")
	fs.StringVar(&cli.inputPath, "list", "", "Alias for -l")
	fs.Var(&cli.domains, "d", "Single domain or word; repeat or comma-separate")
	fs.Var(&cli.domains, "domain", "Alias for -d")
	fs.Var(&cli.tlds, "tld", "TLDs to check (comma-separated); replaces configured TLDs")
	fs.Var(&cli.tldGroups, "tld-group", "TLD group(s) to check: best, medium, common (comma-separated)")
	fs.Var(&cli.proxies, "proxy", "Proxy URL; repeat or comma-separate (e.g. http://localhost:8100)")
	fs.StringVar(&settings.ProxyFile, "proxy-file", "", "File with one proxy URL per line")
	fs.IntVar(&settings.Concurrency, "concurrency", settings.Concurrency, "Concurrent checks")
	fs.IntVar(&settings.Concurrency, "threads", settings.Concurrency, "Alias for -concurrency")
	durationVar(fs, &settings.Timeout, "timeout", "HTTP timeout")
	durationVar(fs, &settings.RateLimit, "rate-limit", "Minimum delay between requests per source key")
	fs.IntVar(&settings.MaxAttempts, "max-attempts", settings.MaxAttempts, "Attempts per domain before giving up")
	fs.BoolVar(&settings.RDAPOnly, "rdap-only", settings.RDAPOnly, "Trust RDAP exclusively (no registrar fallback)")
	fs.BoolVar(&cli.fallback, "fallback", false, "Enable registrar fallback (cloudflare, vercel, hostinger)")
	fs.BoolVar(&settings.BRRDAPOnly, "br-rdap-only", settings.BRRDAPOnly, "Keep .br domains RDAP-only even in fallback mode")
	fs.BoolVar(&settings.ForceRecheck, "force", settings.ForceRecheck, "Ignore the local database and check everything again")
	fs.BoolVar(&settings.ForceRecheck, "force-recheck", settings.ForceRecheck, "Alias for -force")
	fs.BoolVar(&cli.resumeScan, "resume", false, "Resume the previous interrupted scan")
	fs.BoolVar(&cli.noState, "no-state", false, "Do not write resume state for this run")
	fs.BoolVar(&settings.HideRegistered, "hide-registered", settings.HideRegistered, "Hide REGISTERED/RESERVED lines from the console")
	fs.BoolVar(&settings.IncludeInvalid, "include-invalid", settings.IncludeInvalid, "Include invalid input lines in exports")
	fs.BoolVar(&settings.ShortFirst, "short-first", settings.ShortFirst, "Check shortest words first")
	fs.BoolVar(&settings.WordFirst, "word-first", settings.WordFirst, "Iterate words first instead of TLD classes first")
	fs.IntVar(&settings.ExpiringSoonDays, "expiring-days", settings.ExpiringSoonDays, "Days before expiration to flag as expiring soon")
	fs.StringVar(&settings.UserAgent, "user-agent", settings.UserAgent, "HTTP User-Agent")
	fs.StringVar(&cli.outputPath, "o", "", "Also write plain result lines to this file")
	fs.StringVar(&cli.outputPath, "output", "", "Alias for -o")
	fs.BoolVar(&cli.jsonl, "jsonl", false, "Print results as JSON lines instead of text")
	fs.StringVar(&settings.Export.CSV, "csv", settings.Export.CSV, "Write a CSV export at the end")
	fs.StringVar(&settings.Export.JSON, "json", settings.Export.JSON, "Write a JSON export at the end")
	fs.BoolVar(&cli.silent, "silent", false, "Only print result lines (no banner, no logs)")
	fs.BoolVar(&cli.verbose, "v", false, "Verbose output (retries, freezes, rotation)")
	fs.BoolVar(&cli.debug, "debug", false, "Debug output (per-source responses)")
	fs.BoolVar(&cli.noColor, "nc", false, "Disable colored output")
	fs.BoolVar(&cli.noColor, "no-color", false, "Alias for -nc")
	fs.BoolVar(&cli.noNotify, "no-notify", false, "Disable notifications for this run")
	fs.BoolVar(&cli.noProgress, "no-progress", false, "Disable periodic [INF] progress lines (results and other logs stay on)")
	fs.BoolVar(&cli.help, "h", false, "Show help")
	fs.BoolVar(&cli.help, "help", false, "Show help")
}

func applyRunOverrides(settings *config.Settings, cliValues *config.Settings, cli *runCLIFlags, set map[string]bool) {
	if set["concurrency"] || set["threads"] {
		settings.Concurrency = cliValues.Concurrency
	}
	if set["timeout"] {
		settings.Timeout = cliValues.Timeout
	}
	if set["rate-limit"] {
		settings.RateLimit = cliValues.RateLimit
	}
	if set["max-attempts"] {
		settings.MaxAttempts = cliValues.MaxAttempts
	}
	if set["rdap-only"] {
		settings.RDAPOnly = cliValues.RDAPOnly
	}
	if cli.fallback {
		settings.RDAPOnly = false
	}
	if set["br-rdap-only"] {
		settings.BRRDAPOnly = cliValues.BRRDAPOnly
	}
	if set["force"] || set["force-recheck"] {
		settings.ForceRecheck = cliValues.ForceRecheck
	}
	if set["hide-registered"] {
		settings.HideRegistered = cliValues.HideRegistered
	}
	if set["include-invalid"] {
		settings.IncludeInvalid = cliValues.IncludeInvalid
	}
	if set["short-first"] {
		settings.ShortFirst = cliValues.ShortFirst
	}
	if set["word-first"] {
		settings.WordFirst = cliValues.WordFirst
	}
	if set["expiring-days"] {
		settings.ExpiringSoonDays = cliValues.ExpiringSoonDays
	}
	if set["user-agent"] {
		settings.UserAgent = cliValues.UserAgent
	}
	if set["proxy"] {
		settings.Proxies = append([]string(nil), cli.proxies...)
	}
	if set["proxy-file"] {
		settings.ProxyFile = cliValues.ProxyFile
	}
	if set["csv"] {
		settings.Export.CSV = cliValues.Export.CSV
	}
	if set["json"] {
		settings.Export.JSON = cliValues.Export.JSON
	}
	if cli.noProgress {
		settings.ShowProgress = false
	}
}

func resolveTLDs(settings *config.Settings, cli *runCLIFlags, set map[string]bool) error {
	if set["tld"] {
		settings.TLDs = append([]string(nil), cli.tlds...)
		return nil
	}
	if len(cli.tldGroups) > 0 {
		var tlds []string
		for _, group := range cli.tldGroups {
			members, ok := settings.TLDGroups[group]
			if !ok {
				var names []string
				for name := range settings.TLDGroups {
					names = append(names, name)
				}
				sort.Strings(names)
				return fmt.Errorf("unknown TLD group %q (available: %s)", group, strings.Join(names, ", "))
			}
			tlds = append(tlds, members...)
		}
		settings.TLDs = tlds
	}
	return nil
}

func resolveTargets(settings config.Settings, cli *runCLIFlags, positionals []string, stdout io.Writer, printer *output.Printer) ([]types.Target, []types.SkippedInput, bool, error) {
	loadOpts := domainx.LoadOptions{
		TLDs:       settings.TLDs,
		ShortFirst: settings.ShortFirst,
		WordFirst:  settings.WordFirst,
	}

	// -d / -domain flags.
	if len(cli.domains) > 0 {
		return targetsFromValues(cli.domains, loadOpts)
	}

	// -l / positional file.
	inputPath := cli.inputPath
	if inputPath == "" && len(positionals) > 0 {
		if len(positionals) > 1 {
			return nil, nil, false, fmt.Errorf("too many input files: expected one, got %d (%s)", len(positionals), strings.Join(positionals, ", "))
		}
		inputPath = positionals[0]
	}
	if inputPath != "" {
		targets, skipped, err := domainx.LoadFile(inputPath, loadOpts)
		return targets, skipped, false, err
	}

	// Resume without explicit input.
	if cli.resumeScan {
		return nil, nil, true, nil
	}

	// Piped stdin.
	if stdin, ok := stdinReader(); ok {
		targets, skipped, err := domainx.Load(stdin, loadOpts)
		return targets, skipped, false, err
	}

	return nil, nil, false, errors.New("no input: pass a file, -d domain, pipe stdin, or use -resume\nexample: qda run words.txt -config qda.toml")
}

func targetsFromValues(values []string, loadOpts domainx.LoadOptions) ([]types.Target, []types.SkippedInput, bool, error) {
	var targets []types.Target
	var skipped []types.SkippedInput
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if strings.Contains(value, ".") {
			domain, err := domainx.RegistrableDomain(value)
			if err != nil {
				skipped = append(skipped, types.SkippedInput{Input: value, Reason: err.Error()})
				continue
			}
			if !seen[domain] {
				seen[domain] = true
				targets = append(targets, types.Target{Domain: domain, Input: value})
			}
			continue
		}
		word, err := domainx.NormalizeWord(value)
		if err != nil {
			skipped = append(skipped, types.SkippedInput{Input: value, Reason: err.Error()})
			continue
		}
		for _, target := range domainx.ExpandWords([]string{word}, loadOpts.TLDs, loadOpts.WordFirst) {
			if !seen[target.Domain] {
				seen[target.Domain] = true
				targets = append(targets, target)
			}
		}
	}
	return targets, skipped, false, nil
}

func setupResume(settings config.Settings, cli *runCLIFlags, targets []types.Target, wantResume bool, printer *output.Printer) (*resume.Manager, bool, error) {
	if cli.noState {
		if cli.resumeScan {
			return nil, false, errors.New("-resume and -no-state cannot be combined")
		}
		return nil, false, nil
	}
	path := settings.Resume.Path
	if cli.resumeScan || wantResume {
		manager, err := resume.Load(path)
		if err != nil {
			return nil, false, err
		}
		if manager.Finished() {
			// resumed=true with empty pending triggers the "nothing to resume" path.
			return manager, true, nil
		}
		state := manager.State()
		if len(targets) > 0 {
			// Input was also provided: only resume when the target set matches.
			domains := make([]string, 0, len(targets))
			for _, target := range targets {
				domains = append(domains, target.Domain)
			}
			if resume.HashTargets(domains) != state.InputHash {
				printer.Warnf("input differs from the saved scan state; starting a fresh scan")
				fresh := resume.New(path, resume.HashTargets(domains), settings.TLDs, domains)
				return fresh, false, fresh.Save()
			}
		}
		return manager, true, nil
	}

	if resume.Exists(path) {
		if previous, err := resume.Load(path); err == nil && !previous.Finished() {
			state := previous.State()
			printer.Warnf("found unfinished scan state (%d/%d done, saved %s); use -resume to continue or it will be overwritten",
				state.Completed, state.Total, state.UpdatedAt.Format("2006-01-02 15:04:05"))
		}
	}

	domains := make([]string, 0, len(targets))
	for _, target := range targets {
		domains = append(domains, target.Domain)
	}
	manager := resume.New(path, resume.HashTargets(domains), settings.TLDs, domains)
	if err := manager.Save(); err != nil {
		printer.Warnf("could not write resume state: %v", err)
	}
	return manager, false, nil
}

func filterTargets(targets []types.Target, pending []string) []types.Target {
	if len(targets) == 0 {
		out := make([]types.Target, 0, len(pending))
		for _, domain := range pending {
			out = append(out, types.Target{Domain: domain, Input: domain})
		}
		return out
	}
	pendingSet := map[string]bool{}
	for _, domain := range pending {
		pendingSet[domain] = true
	}
	var out []types.Target
	for _, target := range targets {
		if pendingSet[target.Domain] {
			out = append(out, target)
		}
	}
	return out
}

func stdinReader() (io.Reader, bool) {
	info, err := os.Stdin.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice != 0 {
		return nil, false
	}
	return os.Stdin, true
}

func sortResults(results []types.Result) {
	sort.SliceStable(results, func(i, j int) bool {
		left := types.SortRank(results[i])
		right := types.SortRank(results[j])
		if left != right {
			return left < right
		}
		return results[i].Domain < results[j].Domain
	})
}

func qdaVersion() string {
	return version.Version
}

func printRunHelp(out io.Writer, fs *flag.FlagSet) {
	fmt.Fprintf(out, `%s
USAGE
  qda run <words.txt> [options]
  qda run -l words.txt -tld net,org,com
  qda run -d bug.net -d kernel
  cat words.txt | qda run
  qda run -resume

WHAT HAPPENS
  1. Reads words/domains (file, -d, positional or stdin).
  2. Expands words across TLDs in priority order (TLD classes first).
  3. Reuses fresh local-database records for registered domains.
  4. Checks each domain against its authoritative RDAP server.
  5. With -fallback, unknowns are confirmed via cloudflare/vercel/hostinger.
  6. Streams one line per domain as soon as it completes.
  7. Saves the local database, resume state and exports; notifies.

OPTIONS
`, banner.Text(helpColor(out)))
	fs.PrintDefaults()
}
