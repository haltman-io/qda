package qda

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const Version = "0.1.0"

const defaultBootstrapURL = "https://data.iana.org/rdap/dns.json"
const defaultCloudflareAPIBaseURL = "https://api.cloudflare.com/client/v4"
const defaultHostingerAPIBaseURL = "https://developers.hostinger.com"
const defaultVercelAPIBaseURL = "https://api.vercel.com"

type CLIAction int

const (
	ActionRun CLIAction = iota
	ActionInitConfig
	ActionVersion
)

type Settings struct {
	InputPath               string
	ConfigPath              string
	InitConfigPath          string
	TLDs                    []string
	Concurrency             int
	Timeout                 time.Duration
	RateLimit               time.Duration
	SourceRateLimitRetries  int
	SourceRateLimitMaxDelay time.Duration
	UserAgent               string
	ProxyURLs               []string
	ProxyFile               string
	CSVOutput               string
	JSONOutput              string
	RawOutputDir            string
	CacheEnabled            bool
	CachePath               string
	ForceRecheck            bool
	HideRegisteredReserved  bool
	CloudflareAccountID     string
	CloudflareAPIToken      string
	CloudflareAPIBaseURL    string
	CloudflareBatchSize     int
	VercelAPIToken          string
	VercelAPIBaseURL        string
	VercelTeamID            string
	VercelBatchSize         int
	VercelRateLimit         time.Duration
	VercelFetchPrice        bool
	VercelPriceYears        string
	VercelAccounts          []VercelAccount
	HostingerAPIToken       string
	HostingerAPIBaseURL     string
	HostingerRateLimit      time.Duration
	HostingerAccounts       []HostingerAccount
	DiscordWebhookURL       string
	TelegramBotToken        string
	TelegramChatID          string
	TUI                     bool
	IncludeInvalid          bool
	ExpiringSoonDays        int
	BootstrapURL            string
	BootstrapCachePath      string
}

type VercelAccount struct {
	Name     string
	APIToken string
	TeamID   string
}

type HostingerAccount struct {
	Name     string
	APIToken string
}

type fileVercelAccount struct {
	Name     *string `toml:"name"`
	APIToken *string `toml:"api_token"`
	TeamID   *string `toml:"team_id"`
}

type fileHostingerAccount struct {
	Name     *string `toml:"name"`
	APIToken *string `toml:"api_token"`
}

type fileConfig struct {
	TLDs                    []string `toml:"tlds"`
	Concurrency             *int     `toml:"concurrency"`
	Timeout                 *string  `toml:"timeout"`
	RateLimit               *string  `toml:"rate_limit"`
	SourceRateLimitRetries  *int     `toml:"source_rate_limit_retries"`
	SourceRateLimitMaxDelay *string  `toml:"source_rate_limit_max_delay"`
	UserAgent               *string  `toml:"user_agent"`
	Proxies                 []string `toml:"proxies"`
	ProxyFile               *string  `toml:"proxy_file"`
	RawOutputDir            *string  `toml:"raw_output_dir"`
	ForceRecheck            *bool    `toml:"force_recheck"`
	HideRegisteredReserved  *bool    `toml:"hide_registered_reserved"`
	TUI                     *bool    `toml:"tui"`
	IncludeInvalid          *bool    `toml:"include_invalid"`
	ExpiringSoonDays        *int     `toml:"expiring_soon_days"`
	BootstrapURL            *string  `toml:"bootstrap_url"`
	BootstrapCachePath      *string  `toml:"bootstrap_cache_path"`

	Cache struct {
		Enabled *bool   `toml:"enabled"`
		Path    *string `toml:"path"`
	} `toml:"cache"`

	Cloudflare struct {
		AccountID  *string `toml:"account_id"`
		APIToken   *string `toml:"api_token"`
		APIBaseURL *string `toml:"api_base_url"`
		BatchSize  *int    `toml:"batch_size"`
	} `toml:"cloudflare"`

	Vercel struct {
		APIToken   *string             `toml:"api_token"`
		APIBaseURL *string             `toml:"api_base_url"`
		TeamID     *string             `toml:"team_id"`
		BatchSize  *int                `toml:"batch_size"`
		RateLimit  *string             `toml:"rate_limit"`
		FetchPrice *bool               `toml:"fetch_price"`
		PriceYears *string             `toml:"price_years"`
		Accounts   []fileVercelAccount `toml:"accounts"`
	} `toml:"vercel"`

	Hostinger struct {
		APIToken   *string                `toml:"api_token"`
		APIBaseURL *string                `toml:"api_base_url"`
		RateLimit  *string                `toml:"rate_limit"`
		Accounts   []fileHostingerAccount `toml:"accounts"`
	} `toml:"hostinger"`

	Export struct {
		CSV  *string `toml:"csv"`
		JSON *string `toml:"json"`
	} `toml:"export"`

	Discord struct {
		WebhookURL *string `toml:"webhook_url"`
	} `toml:"discord"`

	Telegram struct {
		BotToken *string `toml:"bot_token"`
		ChatID   *string `toml:"chat_id"`
	} `toml:"telegram"`
}

type listFlag []string

func (v *listFlag) String() string {
	return strings.Join(*v, ",")
}

func (v *listFlag) Set(value string) error {
	for _, part := range strings.Split(value, ",") {
		item := strings.TrimSpace(part)
		if item != "" {
			*v = append(*v, item)
		}
	}
	return nil
}

func DefaultSettings() Settings {
	return Settings{
		TLDs:                    []string{"com", "net", "org", "io", "co", "com.br"},
		Concurrency:             2,
		Timeout:                 20 * time.Second,
		RateLimit:               1500 * time.Millisecond,
		SourceRateLimitRetries:  1,
		SourceRateLimitMaxDelay: 5 * time.Second,
		UserAgent:               "qda/" + Version + " (+https://example.invalid/contact)",
		CacheEnabled:            true,
		CloudflareAPIBaseURL:    defaultCloudflareAPIBaseURL,
		CloudflareBatchSize:     20,
		VercelAPIBaseURL:        defaultVercelAPIBaseURL,
		VercelBatchSize:         50,
		VercelRateLimit:         time.Second,
		VercelFetchPrice:        false,
		HostingerAPIBaseURL:     defaultHostingerAPIBaseURL,
		HostingerRateLimit:      6 * time.Second,
		TUI:                     false,
		ExpiringSoonDays:        60,
		BootstrapURL:            defaultBootstrapURL,
	}
}

func ParseCLI(args []string, stderr io.Writer) (CLIAction, Settings, error) {
	settings := DefaultSettings()

	fs := flag.NewFlagSet("qda", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		PrintCLIHelp(stderr, fs)
	}

	var cliTLDs listFlag
	var cliProxies listFlag
	var cliTimeout string
	var cliRateLimit string
	var cliSourceRateLimitMaxDelay string
	var cliVercelRateLimit string
	var cliHostingerRateLimit string
	var cliTUI bool
	var cliNoTUI bool
	var cliVersion bool

	fs.StringVar(&settings.ConfigPath, "config", "", "Path to a TOML configuration file")
	fs.StringVar(&settings.InitConfigPath, "init-config", "", "Write a sample TOML configuration to this path and exit")
	fs.BoolVar(&cliVersion, "version", false, "Print version and exit")
	fs.Var(&cliTLDs, "tld", "TLD/public suffix to check. Repeat or pass comma-separated values")
	fs.IntVar(&settings.Concurrency, "concurrency", settings.Concurrency, "Number of concurrent RDAP checks")
	fs.StringVar(&cliTimeout, "timeout", settings.Timeout.String(), "HTTP timeout, for example 10s")
	fs.StringVar(&cliRateLimit, "rate-limit", settings.RateLimit.String(), "Minimum delay per outbound RDAP request, for example 750ms")
	fs.IntVar(&settings.SourceRateLimitRetries, "source-rate-limit-retries", settings.SourceRateLimitRetries, "Retries after a source returns rate limited")
	fs.StringVar(&cliSourceRateLimitMaxDelay, "source-rate-limit-max-delay", settings.SourceRateLimitMaxDelay.String(), "Maximum wait after a source returns rate limited")
	fs.StringVar(&settings.UserAgent, "user-agent", settings.UserAgent, "HTTP User-Agent sent to RDAP servers")
	fs.Var(&cliProxies, "proxy", "Proxy URL. Repeat or pass comma-separated values")
	fs.StringVar(&settings.ProxyFile, "proxy-file", "", "Text file with one proxy URL per line")
	fs.StringVar(&settings.CSVOutput, "csv", "", "Write results to a CSV file")
	fs.StringVar(&settings.JSONOutput, "json", "", "Write results to a JSON file")
	fs.StringVar(&settings.RawOutputDir, "raw-dir", "", "Directory for raw RDAP JSON responses")
	fs.StringVar(&settings.CachePath, "cache-path", "", "Path to the local result cache JSON file")
	fs.BoolVar(&settings.ForceRecheck, "force-recheck", false, "Ignore cached registered domains and check everything again")
	fs.BoolVar(&settings.HideRegisteredReserved, "hide-registered-reserved", settings.HideRegisteredReserved, "Hide REGISTERED and RESERVED domains from console output")
	fs.StringVar(&settings.CloudflareAccountID, "cloudflare-account-id", "", "Cloudflare account ID for Registrar domain checks")
	fs.StringVar(&settings.CloudflareAPIToken, "cloudflare-api-token", "", "Cloudflare API token for Registrar domain checks")
	fs.StringVar(&settings.CloudflareAPIBaseURL, "cloudflare-api-base-url", settings.CloudflareAPIBaseURL, "Cloudflare API base URL")
	fs.IntVar(&settings.CloudflareBatchSize, "cloudflare-batch-size", settings.CloudflareBatchSize, "Cloudflare domain-check batch size, maximum 20")
	fs.StringVar(&settings.VercelAPIToken, "vercel-api-token", "", "Vercel API token for fallback availability checks")
	fs.StringVar(&settings.VercelAPIBaseURL, "vercel-api-base-url", settings.VercelAPIBaseURL, "Vercel API base URL")
	fs.StringVar(&settings.VercelTeamID, "vercel-team-id", "", "Vercel team ID for Registrar checks")
	fs.IntVar(&settings.VercelBatchSize, "vercel-batch-size", settings.VercelBatchSize, "Vercel availability batch size, maximum 50")
	fs.StringVar(&cliVercelRateLimit, "vercel-rate-limit", settings.VercelRateLimit.String(), "Minimum delay between Vercel fallback batches")
	fs.BoolVar(&settings.VercelFetchPrice, "vercel-fetch-price", settings.VercelFetchPrice, "Fetch Vercel price data for available domains")
	fs.StringVar(&settings.VercelPriceYears, "vercel-price-years", "", "Years query value for Vercel price checks")
	fs.StringVar(&settings.HostingerAPIToken, "hostinger-api-token", "", "Hostinger API token for fallback availability checks")
	fs.StringVar(&settings.HostingerAPIBaseURL, "hostinger-api-base-url", settings.HostingerAPIBaseURL, "Hostinger API base URL")
	fs.StringVar(&cliHostingerRateLimit, "hostinger-rate-limit", settings.HostingerRateLimit.String(), "Minimum delay between Hostinger fallback requests")
	fs.StringVar(&settings.DiscordWebhookURL, "discord-webhook", "", "Discord webhook URL to call after the scan")
	fs.StringVar(&settings.TelegramBotToken, "telegram-token", "", "Telegram bot token")
	fs.StringVar(&settings.TelegramChatID, "telegram-chat", "", "Telegram chat ID")
	fs.BoolVar(&cliTUI, "tui", false, "Enable the interactive terminal UI")
	fs.BoolVar(&cliNoTUI, "no-tui", false, "Disable the interactive terminal UI")
	fs.BoolVar(&settings.IncludeInvalid, "include-invalid", settings.IncludeInvalid, "Include skipped invalid input lines in exports")
	fs.IntVar(&settings.ExpiringSoonDays, "expiring-days", settings.ExpiringSoonDays, "Days before expiration to mark a domain as expiring soon")
	fs.StringVar(&settings.BootstrapURL, "bootstrap-url", settings.BootstrapURL, "IANA RDAP bootstrap URL")
	fs.StringVar(&settings.BootstrapCachePath, "bootstrap-cache", "", "Path for the cached IANA RDAP bootstrap JSON")

	normalizedArgs, err := normalizeCLIArgs(fs, args)
	if err != nil {
		return ActionRun, Settings{}, err
	}

	if err := fs.Parse(normalizedArgs); err != nil {
		return ActionRun, Settings{}, err
	}
	cliSettings := settings

	if settings.ConfigPath != "" {
		if err := applyConfigFile(settings.ConfigPath, &settings); err != nil {
			return ActionRun, Settings{}, err
		}
	}

	if err := overlayCLIFlags(fs, &settings, cliSettings, cliTLDs, cliProxies, cliTimeout, cliRateLimit, cliSourceRateLimitMaxDelay, cliVercelRateLimit, cliHostingerRateLimit, cliTUI, cliNoTUI); err != nil {
		return ActionRun, Settings{}, err
	}

	if cliVersion {
		return ActionVersion, settings, nil
	}

	if settings.InitConfigPath != "" {
		return ActionInitConfig, settings, nil
	}

	if fs.NArg() != 1 {
		return ActionRun, Settings{}, inputArgError(fs.Args())
	}
	settings.InputPath = fs.Arg(0)

	if err := validateSettings(settings); err != nil {
		return ActionRun, Settings{}, err
	}

	return ActionRun, settings, nil
}

func PrintCLIHelp(out io.Writer, fs *flag.FlagSet) {
	fmt.Fprint(out, `QDA - Quick Domain Availability
RDAP pre-check + Cloudflare Registrar availability + local cache.

USAGE
  qda <words.txt> [options]
  qda [options] <words.txt>

FAST PATH
  qda a.txt -config qda.toml

WHAT HAPPENS
  1. Reads words/domains from the .txt file.
  2. Expands plain words across configured TLDs.
  3. Uses RDAP first to skip domains already registered.
  4. Sends only unresolved candidates to Cloudflare.
  5. Sends Cloudflare UNKNOWN domains to Vercel, then Hostinger if needed.
  6. Prints a colored live log and a final prioritized table.
  7. Saves cache and exports locally when configured.

RESULT PRIORITY
  AVAILABLE / AVAILABLE PREMIUM
  AVAILABLE SOON
  RATE LIMITED / UNKNOWN / RESERVED
  REGISTERED

COMMON COMMANDS
  qda a.txt -config qda.toml
      Use the local config file.

  qda a.txt -config qda.toml -force-recheck
      Ignore cached registered domains and check again.

  qda a.txt -config qda.toml -tld com,net,org
      Override configured TLDs for this run only.

  qda -init-config qda.toml
      Write a sample config file. It will not overwrite an existing file.

CONFIG REQUIRED FOR LIVE CHECKS
  qda.toml:

    [cloudflare]
    account_id = "..."
    api_token = "..."

    [hostinger]
    api_token = "..."   # optional fallback for Cloudflare unknown results
    # For rotation, add [[hostinger.accounts]] entries with api_token.

    [vercel]
    api_token = "..."   # optional fallback for Cloudflare unknown results
    # For rotation, add [[vercel.accounts]] entries with api_token/team_id.

  Or pass:
    -cloudflare-account-id ...
    -cloudflare-api-token ...
    -vercel-api-token ...
    -hostinger-api-token ...

OPTIONS
`)
	fs.PrintDefaults()
	fmt.Fprint(out, `
NOTES
  Flags may be placed before or after the input file.
  Example: qda a.txt -config qda.toml

  Cached registered domains are skipped until expiration passes.
  Available domains are checked again because they can be registered by someone else.
  Vercel is only queried for domains that Cloudflare leaves unknown.
  Hostinger is only queried for domains that Cloudflare and Vercel leave unknown.
  Use --hide-registered-reserved to keep non-actionable domains out of the console log.
`)
}

func inputArgError(args []string) error {
	switch len(args) {
	case 0:
		return errors.New("input file missing: pass one .txt file with words/domains\nexample: qda a.txt -config qda.toml\nhelp: qda --help")
	case 1:
		return nil
	default:
		return fmt.Errorf("too many input files: expected one .txt file, got %d (%s)\nexample: qda a.txt -config qda.toml\nhelp: qda --help", len(args), strings.Join(args, ", "))
	}
}

func normalizeCLIArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	flags := make([]string, 0, len(args))
	positionals := make([]string, 0, len(args))

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if !isFlagToken(arg) {
			positionals = append(positionals, arg)
			continue
		}

		name := cliFlagName(arg)
		registered := fs.Lookup(name)
		if registered == nil {
			flags = append(flags, arg)
			continue
		}

		flags = append(flags, arg)
		if strings.Contains(arg, "=") {
			continue
		}
		if cliFlagIsBool(registered) {
			if i+1 < len(args) && isBoolLiteral(args[i+1]) {
				i++
				flags[len(flags)-1] = arg + "=" + args[i]
			}
			continue
		}
		if i+1 >= len(args) {
			return nil, fmt.Errorf("flag needs an argument: -%s", name)
		}
		i++
		flags = append(flags, args[i])
	}

	return append(flags, positionals...), nil
}

func isFlagToken(value string) bool {
	return strings.HasPrefix(value, "-") && value != "-"
}

func cliFlagName(value string) string {
	value = strings.TrimLeft(value, "-")
	if name, _, ok := strings.Cut(value, "="); ok {
		return name
	}
	return value
}

func cliFlagIsBool(flag *flag.Flag) bool {
	boolFlag, ok := flag.Value.(interface{ IsBoolFlag() bool })
	return ok && boolFlag.IsBoolFlag()
}

func isBoolLiteral(value string) bool {
	switch strings.ToLower(value) {
	case "1", "0", "t", "f", "true", "false":
		return true
	default:
		return false
	}
}

func overlayCLIFlags(fs *flag.FlagSet, settings *Settings, cli Settings, tlds listFlag, proxies listFlag, timeoutValue string, rateLimitValue string, sourceRateLimitMaxDelayValue string, vercelRateLimitValue string, hostingerRateLimitValue string, tui bool, noTUI bool) error {
	seen := map[string]bool{}
	fs.Visit(func(f *flag.Flag) {
		seen[f.Name] = true
	})

	if seen["init-config"] {
		settings.InitConfigPath = cli.InitConfigPath
	}
	if seen["tld"] {
		settings.TLDs = append([]string(nil), tlds...)
	}
	if seen["concurrency"] {
		settings.Concurrency = cli.Concurrency
	}
	if seen["proxy"] {
		settings.ProxyURLs = append([]string(nil), proxies...)
	}
	if seen["proxy-file"] {
		settings.ProxyFile = cli.ProxyFile
	}
	if seen["timeout"] {
		timeout, err := time.ParseDuration(timeoutValue)
		if err != nil {
			return fmt.Errorf("invalid --timeout: %w", err)
		}
		settings.Timeout = timeout
	}
	if seen["rate-limit"] {
		rateLimit, err := time.ParseDuration(rateLimitValue)
		if err != nil {
			return fmt.Errorf("invalid --rate-limit: %w", err)
		}
		settings.RateLimit = rateLimit
	}
	if seen["source-rate-limit-retries"] {
		settings.SourceRateLimitRetries = cli.SourceRateLimitRetries
	}
	if seen["source-rate-limit-max-delay"] {
		maxDelay, err := time.ParseDuration(sourceRateLimitMaxDelayValue)
		if err != nil {
			return fmt.Errorf("invalid --source-rate-limit-max-delay: %w", err)
		}
		settings.SourceRateLimitMaxDelay = maxDelay
	}
	if seen["tui"] {
		settings.TUI = tui
	}
	if noTUI {
		settings.TUI = false
	}
	if seen["user-agent"] {
		settings.UserAgent = cli.UserAgent
	}
	if seen["csv"] {
		settings.CSVOutput = cli.CSVOutput
	}
	if seen["json"] {
		settings.JSONOutput = cli.JSONOutput
	}
	if seen["raw-dir"] {
		settings.RawOutputDir = cli.RawOutputDir
	}
	if seen["cache-path"] {
		settings.CachePath = cli.CachePath
	}
	if seen["force-recheck"] {
		settings.ForceRecheck = cli.ForceRecheck
	}
	if seen["hide-registered-reserved"] {
		settings.HideRegisteredReserved = cli.HideRegisteredReserved
	}
	if seen["cloudflare-account-id"] {
		settings.CloudflareAccountID = cli.CloudflareAccountID
	}
	if seen["cloudflare-api-token"] {
		settings.CloudflareAPIToken = cli.CloudflareAPIToken
	}
	if seen["cloudflare-api-base-url"] {
		settings.CloudflareAPIBaseURL = cli.CloudflareAPIBaseURL
	}
	if seen["cloudflare-batch-size"] {
		settings.CloudflareBatchSize = cli.CloudflareBatchSize
	}
	if seen["vercel-api-token"] {
		settings.VercelAPIToken = cli.VercelAPIToken
	}
	if seen["vercel-api-base-url"] {
		settings.VercelAPIBaseURL = cli.VercelAPIBaseURL
	}
	if seen["vercel-team-id"] {
		settings.VercelTeamID = cli.VercelTeamID
	}
	if seen["vercel-batch-size"] {
		settings.VercelBatchSize = cli.VercelBatchSize
	}
	if seen["vercel-rate-limit"] {
		rateLimit, err := time.ParseDuration(vercelRateLimitValue)
		if err != nil {
			return fmt.Errorf("invalid --vercel-rate-limit: %w", err)
		}
		settings.VercelRateLimit = rateLimit
	}
	if seen["vercel-fetch-price"] {
		settings.VercelFetchPrice = cli.VercelFetchPrice
	}
	if seen["vercel-price-years"] {
		settings.VercelPriceYears = cli.VercelPriceYears
	}
	if seen["hostinger-api-token"] {
		settings.HostingerAPIToken = cli.HostingerAPIToken
	}
	if seen["hostinger-api-base-url"] {
		settings.HostingerAPIBaseURL = cli.HostingerAPIBaseURL
	}
	if seen["hostinger-rate-limit"] {
		rateLimit, err := time.ParseDuration(hostingerRateLimitValue)
		if err != nil {
			return fmt.Errorf("invalid --hostinger-rate-limit: %w", err)
		}
		settings.HostingerRateLimit = rateLimit
	}
	if seen["discord-webhook"] {
		settings.DiscordWebhookURL = cli.DiscordWebhookURL
	}
	if seen["telegram-token"] {
		settings.TelegramBotToken = cli.TelegramBotToken
	}
	if seen["telegram-chat"] {
		settings.TelegramChatID = cli.TelegramChatID
	}
	if seen["include-invalid"] {
		settings.IncludeInvalid = cli.IncludeInvalid
	}
	if seen["expiring-days"] {
		settings.ExpiringSoonDays = cli.ExpiringSoonDays
	}
	if seen["bootstrap-url"] {
		settings.BootstrapURL = cli.BootstrapURL
	}
	if seen["bootstrap-cache"] {
		settings.BootstrapCachePath = cli.BootstrapCachePath
	}
	return nil
}

func applyConfigFile(path string, settings *Settings) error {
	var cfg fileConfig
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if cfg.TLDs != nil {
		settings.TLDs = append([]string(nil), cfg.TLDs...)
	}
	if cfg.Concurrency != nil {
		settings.Concurrency = *cfg.Concurrency
	}
	if cfg.Timeout != nil {
		timeout, err := time.ParseDuration(*cfg.Timeout)
		if err != nil {
			return fmt.Errorf("invalid config timeout: %w", err)
		}
		settings.Timeout = timeout
	}
	if cfg.RateLimit != nil {
		rateLimit, err := time.ParseDuration(*cfg.RateLimit)
		if err != nil {
			return fmt.Errorf("invalid config rate_limit: %w", err)
		}
		settings.RateLimit = rateLimit
	}
	if cfg.SourceRateLimitRetries != nil {
		settings.SourceRateLimitRetries = *cfg.SourceRateLimitRetries
	}
	if cfg.SourceRateLimitMaxDelay != nil {
		maxDelay, err := time.ParseDuration(*cfg.SourceRateLimitMaxDelay)
		if err != nil {
			return fmt.Errorf("invalid config source_rate_limit_max_delay: %w", err)
		}
		settings.SourceRateLimitMaxDelay = maxDelay
	}
	if cfg.UserAgent != nil {
		settings.UserAgent = *cfg.UserAgent
	}
	if cfg.Proxies != nil {
		settings.ProxyURLs = append([]string(nil), cfg.Proxies...)
	}
	if cfg.ProxyFile != nil {
		settings.ProxyFile = *cfg.ProxyFile
	}
	if cfg.RawOutputDir != nil {
		settings.RawOutputDir = *cfg.RawOutputDir
	}
	if cfg.ForceRecheck != nil {
		settings.ForceRecheck = *cfg.ForceRecheck
	}
	if cfg.HideRegisteredReserved != nil {
		settings.HideRegisteredReserved = *cfg.HideRegisteredReserved
	}
	if cfg.TUI != nil {
		settings.TUI = *cfg.TUI
	}
	if cfg.IncludeInvalid != nil {
		settings.IncludeInvalid = *cfg.IncludeInvalid
	}
	if cfg.ExpiringSoonDays != nil {
		settings.ExpiringSoonDays = *cfg.ExpiringSoonDays
	}
	if cfg.BootstrapURL != nil {
		settings.BootstrapURL = *cfg.BootstrapURL
	}
	if cfg.BootstrapCachePath != nil {
		settings.BootstrapCachePath = *cfg.BootstrapCachePath
	}
	if cfg.Cache.Enabled != nil {
		settings.CacheEnabled = *cfg.Cache.Enabled
	}
	if cfg.Cache.Path != nil {
		settings.CachePath = *cfg.Cache.Path
	}
	if cfg.Cloudflare.AccountID != nil {
		settings.CloudflareAccountID = *cfg.Cloudflare.AccountID
	}
	if cfg.Cloudflare.APIToken != nil {
		settings.CloudflareAPIToken = *cfg.Cloudflare.APIToken
	}
	if cfg.Cloudflare.APIBaseURL != nil {
		settings.CloudflareAPIBaseURL = *cfg.Cloudflare.APIBaseURL
	}
	if cfg.Cloudflare.BatchSize != nil {
		settings.CloudflareBatchSize = *cfg.Cloudflare.BatchSize
	}
	if cfg.Vercel.APIToken != nil {
		settings.VercelAPIToken = *cfg.Vercel.APIToken
	}
	if cfg.Vercel.APIBaseURL != nil {
		settings.VercelAPIBaseURL = *cfg.Vercel.APIBaseURL
	}
	if cfg.Vercel.TeamID != nil {
		settings.VercelTeamID = *cfg.Vercel.TeamID
	}
	if cfg.Vercel.BatchSize != nil {
		settings.VercelBatchSize = *cfg.Vercel.BatchSize
	}
	if cfg.Vercel.RateLimit != nil {
		rateLimit, err := time.ParseDuration(*cfg.Vercel.RateLimit)
		if err != nil {
			return fmt.Errorf("invalid vercel rate_limit: %w", err)
		}
		settings.VercelRateLimit = rateLimit
	}
	if cfg.Vercel.FetchPrice != nil {
		settings.VercelFetchPrice = *cfg.Vercel.FetchPrice
	}
	if cfg.Vercel.PriceYears != nil {
		settings.VercelPriceYears = *cfg.Vercel.PriceYears
	}
	if cfg.Vercel.Accounts != nil {
		settings.VercelAccounts = make([]VercelAccount, 0, len(cfg.Vercel.Accounts))
		for _, account := range cfg.Vercel.Accounts {
			item := VercelAccount{}
			if account.Name != nil {
				item.Name = *account.Name
			}
			if account.APIToken != nil {
				item.APIToken = *account.APIToken
			}
			if account.TeamID != nil {
				item.TeamID = *account.TeamID
			}
			settings.VercelAccounts = append(settings.VercelAccounts, item)
		}
	}
	if cfg.Hostinger.APIToken != nil {
		settings.HostingerAPIToken = *cfg.Hostinger.APIToken
	}
	if cfg.Hostinger.APIBaseURL != nil {
		settings.HostingerAPIBaseURL = *cfg.Hostinger.APIBaseURL
	}
	if cfg.Hostinger.RateLimit != nil {
		rateLimit, err := time.ParseDuration(*cfg.Hostinger.RateLimit)
		if err != nil {
			return fmt.Errorf("invalid hostinger rate_limit: %w", err)
		}
		settings.HostingerRateLimit = rateLimit
	}
	if cfg.Hostinger.Accounts != nil {
		settings.HostingerAccounts = make([]HostingerAccount, 0, len(cfg.Hostinger.Accounts))
		for _, account := range cfg.Hostinger.Accounts {
			item := HostingerAccount{}
			if account.Name != nil {
				item.Name = *account.Name
			}
			if account.APIToken != nil {
				item.APIToken = *account.APIToken
			}
			settings.HostingerAccounts = append(settings.HostingerAccounts, item)
		}
	}
	if cfg.Export.CSV != nil {
		settings.CSVOutput = *cfg.Export.CSV
	}
	if cfg.Export.JSON != nil {
		settings.JSONOutput = *cfg.Export.JSON
	}
	if cfg.Discord.WebhookURL != nil {
		settings.DiscordWebhookURL = *cfg.Discord.WebhookURL
	}
	if cfg.Telegram.BotToken != nil {
		settings.TelegramBotToken = *cfg.Telegram.BotToken
	}
	if cfg.Telegram.ChatID != nil {
		settings.TelegramChatID = *cfg.Telegram.ChatID
	}

	return nil
}

func validateSettings(settings Settings) error {
	if strings.TrimSpace(settings.InputPath) == "" {
		return errors.New("input file missing: pass one .txt file with words/domains\nexample: qda a.txt -config qda.toml")
	}
	if settings.Concurrency < 1 {
		return errors.New("invalid concurrency: use a value greater than zero\nexample: -concurrency 4")
	}
	if settings.Timeout <= 0 {
		return errors.New("invalid timeout: use a duration greater than zero\nexample: -timeout 12s")
	}
	if settings.RateLimit < 0 {
		return errors.New("invalid rate limit: duration cannot be negative\nexample: -rate-limit 250ms")
	}
	if settings.SourceRateLimitRetries < 0 {
		return errors.New("invalid source rate limit retries: value cannot be negative\nexample: -source-rate-limit-retries 3")
	}
	if settings.SourceRateLimitMaxDelay < 0 {
		return errors.New("invalid source rate limit max delay: duration cannot be negative\nexample: -source-rate-limit-max-delay 5s")
	}
	if strings.TrimSpace(settings.UserAgent) == "" {
		return errors.New("user agent missing: set user_agent in qda.toml or pass -user-agent")
	}
	if len(settings.TLDs) == 0 {
		return errors.New("no TLDs configured: add tlds to qda.toml or pass -tld com,net,org")
	}
	if settings.ExpiringSoonDays < 0 {
		return errors.New("invalid expiring-days: value cannot be negative\nexample: -expiring-days 60")
	}
	if settings.CloudflareAccountID == "" {
		return errors.New("Cloudflare account ID missing: set [cloudflare] account_id in qda.toml or pass -cloudflare-account-id")
	}
	if settings.CloudflareAPIToken == "" {
		return errors.New("Cloudflare API token missing: set [cloudflare] api_token in qda.toml or pass -cloudflare-api-token")
	}
	if strings.TrimSpace(settings.CloudflareAPIBaseURL) == "" {
		return errors.New("Cloudflare API base URL missing: set [cloudflare] api_base_url or pass -cloudflare-api-base-url")
	}
	if settings.CloudflareBatchSize < 1 || settings.CloudflareBatchSize > 20 {
		return errors.New("invalid Cloudflare batch size: use a value from 1 to 20\nexample: -cloudflare-batch-size 20")
	}
	if strings.TrimSpace(settings.VercelAPIBaseURL) == "" {
		return errors.New("Vercel API base URL missing: set [vercel] api_base_url or pass -vercel-api-base-url")
	}
	if settings.VercelBatchSize < 1 || settings.VercelBatchSize > 50 {
		return errors.New("invalid Vercel batch size: use a value from 1 to 50\nexample: -vercel-batch-size 50")
	}
	if settings.VercelRateLimit < 0 {
		return errors.New("invalid Vercel rate limit: duration cannot be negative\nexample: [vercel] rate_limit = \"1s\"")
	}
	if strings.TrimSpace(settings.HostingerAPIBaseURL) == "" {
		return errors.New("Hostinger API base URL missing: set [hostinger] api_base_url or pass -hostinger-api-base-url")
	}
	if settings.HostingerRateLimit < 0 {
		return errors.New("invalid Hostinger rate limit: duration cannot be negative\nexample: [hostinger] rate_limit = \"6s\"")
	}
	return nil
}

func WriteSampleConfig(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("config path is required")
	}
	if err := os.MkdirAll(filepath.Dir(cleanPath(path)), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("config file already exists: %s\nrefusing to overwrite it; edit the existing file or choose another path", path)
		}
		return err
	}
	defer file.Close()
	_, err = file.WriteString(sampleConfig)
	return err
}

func cleanPath(path string) string {
	dir := filepath.Dir(path)
	if dir == "." || dir == "" {
		return "."
	}
	return path
}

const sampleConfig = `# qda sample configuration
# CLI flags override these values without rewriting this file.

tlds = ["com", "net", "org", "io", "co", "com.br"]
concurrency = 4
timeout = "12s"
rate_limit = "250ms"
source_rate_limit_retries = 1
source_rate_limit_max_delay = "5s"
user_agent = "qda/0.1.0 (+mailto:you@example.com)"
tui = false
include_invalid = false
force_recheck = false
hide_registered_reserved = false
expiring_soon_days = 60
bootstrap_url = "https://data.iana.org/rdap/dns.json"
bootstrap_cache_path = ".qda-cache/rdap-dns.json"

# Optional HTTP/HTTPS proxy rotation.
proxies = []
proxy_file = ""
raw_output_dir = ".qda-cache/rdap-raw"

[cache]
enabled = true
path = ".qda-cache/results.json"

[cloudflare]
account_id = ""
api_token = ""
api_base_url = "https://api.cloudflare.com/client/v4"
batch_size = 20

[vercel]
api_token = ""
api_base_url = "https://api.vercel.com"
team_id = ""
batch_size = 50
rate_limit = "1s"
fetch_price = false
price_years = ""

[[vercel.accounts]]
name = "vercel-2"
api_token = ""
team_id = ""

[[vercel.accounts]]
name = "vercel-3"
api_token = ""
team_id = ""

[hostinger]
api_token = ""
api_base_url = "https://developers.hostinger.com"
rate_limit = "6s"

[[hostinger.accounts]]
name = "hostinger-2"
api_token = ""

[[hostinger.accounts]]
name = "hostinger-3"
api_token = ""

[export]
csv = ".qda-output/results.csv"
json = ".qda-output/results.json"

[discord]
webhook_url = ""

[telegram]
bot_token = ""
chat_id = ""
`
