// Package config defines qda settings, TOML loading and validation.
package config

import (
	"errors"
	"fmt"
	"time"

	"github.com/BurntSushi/toml"

	"qda/internal/version"
)

const (
	defaultBootstrapURL        = "https://data.iana.org/rdap/dns.json"
	defaultCloudflareAPIBase   = "https://api.cloudflare.com/client/v4"
	defaultHostingerAPIBase    = "https://developers.hostinger.com"
	defaultVercelAPIBase       = "https://api.vercel.com"
	defaultStorePath           = ".qda-cache/qda.db.json"
	defaultResumePath          = ".qda-cache/state.json"
	defaultBootstrapCachePath  = ".qda-cache/rdap-dns.json"
	defaultListen              = "127.0.0.1:8080"
	defaultTelegramAPIBase     = "https://api.telegram.org"
	defaultGitHubAPIBase       = "https://api.github.com"
)

// VercelAccount is one Vercel credential set used for rotation.
type VercelAccount struct {
	Name     string
	APIToken string
	TeamID   string
}

// HostingerAccount is one Hostinger credential set used for rotation.
type HostingerAccount struct {
	Name     string
	APIToken string
}

// RDAPSettings controls the RDAP source.
type RDAPSettings struct {
	BootstrapURL       string
	BootstrapCachePath string
	BootstrapRefresh   bool
	RawOutputDir       string
	RateLimit          time.Duration // 0 means: use the global rate limit
}

// CloudflareSettings controls the Cloudflare Registrar source.
type CloudflareSettings struct {
	AccountID  string
	APIToken   string
	APIBaseURL string
	RateLimit  time.Duration
}

// VercelSettings controls the Vercel Registrar source.
type VercelSettings struct {
	APIToken   string
	APIBaseURL string
	TeamID     string
	RateLimit  time.Duration
	FetchPrice bool
	PriceYears string
	Accounts   []VercelAccount
}

// HostingerSettings controls the Hostinger availability source.
type HostingerSettings struct {
	APIToken   string
	APIBaseURL string
	RateLimit  time.Duration
	Accounts   []HostingerAccount
}

// StoreSettings controls the local results database.
type StoreSettings struct {
	Enabled       bool
	Path          string
	RegisteredTTL time.Duration
	ReservedTTL   time.Duration
}

// ResumeSettings controls scan state persistence.
type ResumeSettings struct {
	Path string
}

// ExportSettings controls end-of-scan file exports.
type ExportSettings struct {
	CSV  string
	JSON string
}

// EmailSettings controls SMTP notifications.
type EmailSettings struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	To       []string
	TLSMode  string // starttls (default), tls, none
}

// GitHubSettings controls GitHub issue notifications.
type GitHubSettings struct {
	Token   string
	Owner   string
	Repo    string
	APIBase string
}

// NotifySettings aggregates every notification channel.
type NotifySettings struct {
	OnFinish    bool
	OnAvailable bool
	DiscordWebhook  string
	TelegramToken   string
	TelegramChatID  string
	TelegramAPIBase string
	SlackWebhook    string
	WebhookURL      string
	Email           EmailSettings
	GitHub          GitHubSettings
}

// APISettings controls the HTTP server mode.
type APISettings struct {
	Listen    string
	AuthToken string
}

// Settings is the fully-resolved qda configuration.
type Settings struct {
	TLDs              []string
	TLDGroups         map[string][]string
	Concurrency       int
	Timeout           time.Duration
	RateLimit         time.Duration
	MaxAttempts       int
	NetworkRetries    int
	NetworkRetryDelay time.Duration
	SourceFreeze      time.Duration
	SourceMaxDelay    time.Duration
	ProgressInterval  time.Duration
	ShowProgress      bool // periodic [INF] progress lines on stderr
	UserAgent         string
	Proxies           []string
	ProxyFile         string
	RDAPOnly          bool
	BRRDAPOnly        bool
	HideRegistered    bool
	IncludeInvalid    bool
	ForceRecheck      bool
	ShortFirst        bool
	WordFirst         bool
	ExpiringSoonDays  int

	RDAP       RDAPSettings
	Cloudflare CloudflareSettings
	Vercel     VercelSettings
	Hostinger  HostingerSettings
	Store      StoreSettings
	Resume     ResumeSettings
	Export     ExportSettings
	Notify     NotifySettings
	API        APISettings
}

// Default returns sane defaults inspired by mature CLI tools.
func Default() Settings {
	return Settings{
		TLDs: []string{"net", "org", "com", "io", "to", "lat", "sh"},
		TLDGroups: map[string][]string{
			"best":   {"net", "org", "com"},
			"medium": {"io", "to", "lat", "sh"},
			"common": {"is", "me", "my", "lol", "nl", "ph", "gg", "cc", "eu", "in", "id", "ch", "ws"},
		},
		Concurrency:       8,
		Timeout:           12 * time.Second,
		RateLimit:         500 * time.Millisecond,
		MaxAttempts:       4,
		NetworkRetries:    3,
		NetworkRetryDelay: 2 * time.Second,
		SourceFreeze:      15 * time.Minute,
		SourceMaxDelay:    0,
		ProgressInterval:  15 * time.Second,
		ShowProgress:      true,
		UserAgent:         "qda/" + version.Version + " (+https://github.com/qda)",
		RDAPOnly:          true,
		BRRDAPOnly:        true,
		ExpiringSoonDays:  30,
		RDAP: RDAPSettings{
			BootstrapURL:       defaultBootstrapURL,
			BootstrapCachePath: defaultBootstrapCachePath,
			BootstrapRefresh:   true,
		},
		Cloudflare: CloudflareSettings{
			APIBaseURL: defaultCloudflareAPIBase,
			RateLimit:  2 * time.Second,
		},
		Vercel: VercelSettings{
			APIBaseURL: defaultVercelAPIBase,
			RateLimit:  time.Second,
		},
		Hostinger: HostingerSettings{
			APIBaseURL: defaultHostingerAPIBase,
			RateLimit:  6 * time.Second,
		},
		Store: StoreSettings{
			Enabled:       true,
			Path:          defaultStorePath,
			RegisteredTTL: 168 * time.Hour,
			ReservedTTL:   720 * time.Hour,
		},
		Resume: ResumeSettings{Path: defaultResumePath},
		Notify: NotifySettings{
			OnFinish:        true,
			TelegramAPIBase: defaultTelegramAPIBase,
		},
		API: APISettings{
			Listen: defaultListen,
		},
	}
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

// fileConfig mirrors the TOML document. Pointer fields let us distinguish
// "absent" from "zero value". Legacy keys ([cache], [discord], [telegram]
// and the old top-level bootstrap_*/raw_output_dir keys) are still accepted.
type fileConfig struct {
	TLDs              []string          `toml:"tlds"`
	TLDGroups         map[string][]string `toml:"tld_groups"`
	Concurrency       *int              `toml:"concurrency"`
	Timeout           *string           `toml:"timeout"`
	RateLimit         *string           `toml:"rate_limit"`
	MaxAttempts       *int              `toml:"max_attempts"`
	NetworkRetries    *int              `toml:"network_retries"`
	NetworkRetryDelay *string           `toml:"network_retry_delay"`
	SourceFreeze      *string           `toml:"source_freeze"`
	SourceMaxDelay    *string           `toml:"source_rate_limit_max_delay"`
	ProgressInterval  *string           `toml:"progress_interval"`
	ShowProgress      *bool             `toml:"show_progress"`
	UserAgent         *string           `toml:"user_agent"`
	Proxies           []string          `toml:"proxies"`
	ProxyFile         *string           `toml:"proxy_file"`
	RDAPOnly          *bool             `toml:"rdap_only"`
	BRRDAPOnly        *bool             `toml:"br_rdap_only"`
	HideRegistered    *bool             `toml:"hide_registered_reserved"`
	IncludeInvalid    *bool             `toml:"include_invalid"`
	ForceRecheck      *bool             `toml:"force_recheck"`
	ShortFirst        *bool             `toml:"short_first"`
	WordFirst         *bool             `toml:"word_first"`
	ExpiringSoonDays  *int              `toml:"expiring_soon_days"`

	// Legacy top-level keys (kept for backwards compatibility).
	BootstrapURL       *string  `toml:"bootstrap_url"`
	BootstrapCachePath *string  `toml:"bootstrap_cache_path"`
	BootstrapRefresh   *bool    `toml:"bootstrap_refresh"`
	RawOutputDir       *string  `toml:"raw_output_dir"`
	SourceRateLimitRetries *int  `toml:"source_rate_limit_retries"`

	RDAP struct {
		BootstrapURL       *string `toml:"bootstrap_url"`
		BootstrapCachePath *string `toml:"bootstrap_cache_path"`
		BootstrapRefresh   *bool   `toml:"bootstrap_refresh"`
		RawOutputDir       *string `toml:"raw_output_dir"`
		RateLimit          *string `toml:"rate_limit"`
	} `toml:"rdap"`

	Store struct {
		Enabled       *bool   `toml:"enabled"`
		Path          *string `toml:"path"`
		RegisteredTTL *string `toml:"registered_ttl"`
		ReservedTTL   *string `toml:"reserved_ttl"`
	} `toml:"store"`

	// Legacy [cache] section.
	Cache struct {
		Enabled *bool   `toml:"enabled"`
		Path    *string `toml:"path"`
	} `toml:"cache"`

	Resume struct {
		Path *string `toml:"path"`
	} `toml:"resume"`

	Export struct {
		CSV  *string `toml:"csv"`
		JSON *string `toml:"json"`
	} `toml:"export"`

	Cloudflare struct {
		AccountID  *string `toml:"account_id"`
		APIToken   *string `toml:"api_token"`
		APIBaseURL *string `toml:"api_base_url"`
		RateLimit  *string `toml:"rate_limit"`
	} `toml:"cloudflare"`

	Vercel struct {
		APIToken   *string             `toml:"api_token"`
		APIBaseURL *string             `toml:"api_base_url"`
		TeamID     *string             `toml:"team_id"`
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

	API struct {
		Listen    *string `toml:"listen"`
		AuthToken *string `toml:"auth_token"`
	} `toml:"api"`

	Notify struct {
		OnFinish    *bool `toml:"on_finish"`
		OnAvailable *bool `toml:"on_available"`
		Discord struct {
			WebhookURL *string `toml:"webhook_url"`
		} `toml:"discord"`
		Telegram struct {
			BotToken *string `toml:"bot_token"`
			ChatID   *string `toml:"chat_id"`
			APIBase  *string `toml:"api_base_url"`
		} `toml:"telegram"`
		Slack struct {
			WebhookURL *string `toml:"webhook_url"`
		} `toml:"slack"`
		Webhook struct {
			URL *string `toml:"url"`
		} `toml:"webhook"`
		Email struct {
			Host     *string  `toml:"host"`
			Port     *int     `toml:"port"`
			Username *string  `toml:"username"`
			Password *string  `toml:"password"`
			From     *string  `toml:"from"`
			To       []string `toml:"to"`
			TLSMode  *string  `toml:"tls"`
		} `toml:"email"`
		GitHub struct {
			Token   *string `toml:"token"`
			Owner   *string `toml:"owner"`
			Repo    *string `toml:"repo"`
			APIBase *string `toml:"api_base_url"`
		} `toml:"github"`
	} `toml:"notify"`

	// Legacy notification sections.
	Discord struct {
		WebhookURL *string `toml:"webhook_url"`
	} `toml:"discord"`
	Telegram struct {
		BotToken *string `toml:"bot_token"`
		ChatID   *string `toml:"chat_id"`
	} `toml:"telegram"`
}

// LoadFile overlays a TOML file on top of the given settings.
func LoadFile(path string, settings *Settings) error {
	var cfg fileConfig
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	return applyFileConfig(cfg, settings)
}

func applyFileConfig(cfg fileConfig, s *Settings) error {
	var err error

	if cfg.TLDs != nil {
		s.TLDs = append([]string(nil), cfg.TLDs...)
	}
	if cfg.TLDGroups != nil {
		if s.TLDGroups == nil {
			s.TLDGroups = map[string][]string{}
		}
		for name, tlds := range cfg.TLDGroups {
			s.TLDGroups[name] = tlds
		}
	}
	if cfg.Concurrency != nil {
		s.Concurrency = *cfg.Concurrency
	}
	if err = applyDuration(&s.Timeout, cfg.Timeout, "timeout"); err != nil {
		return err
	}
	if err = applyDuration(&s.RateLimit, cfg.RateLimit, "rate_limit"); err != nil {
		return err
	}
	if cfg.MaxAttempts != nil {
		s.MaxAttempts = *cfg.MaxAttempts
	}
	// Legacy alias: source_rate_limit_retries ≈ max_attempts-1 for sources.
	if cfg.MaxAttempts == nil && cfg.SourceRateLimitRetries != nil {
		s.MaxAttempts = *cfg.SourceRateLimitRetries + 1
	}
	if cfg.NetworkRetries != nil {
		s.NetworkRetries = *cfg.NetworkRetries
	}
	if err = applyDuration(&s.NetworkRetryDelay, cfg.NetworkRetryDelay, "network_retry_delay"); err != nil {
		return err
	}
	if err = applyDuration(&s.SourceFreeze, cfg.SourceFreeze, "source_freeze"); err != nil {
		return err
	}
	if err = applyDuration(&s.SourceMaxDelay, cfg.SourceMaxDelay, "source_rate_limit_max_delay"); err != nil {
		return err
	}
	if err = applyDuration(&s.ProgressInterval, cfg.ProgressInterval, "progress_interval"); err != nil {
		return err
	}
	if cfg.ShowProgress != nil {
		s.ShowProgress = *cfg.ShowProgress
	}
	if cfg.UserAgent != nil {
		s.UserAgent = *cfg.UserAgent
	}
	if cfg.Proxies != nil {
		s.Proxies = append([]string(nil), cfg.Proxies...)
	}
	if cfg.ProxyFile != nil {
		s.ProxyFile = *cfg.ProxyFile
	}
	if cfg.RDAPOnly != nil {
		s.RDAPOnly = *cfg.RDAPOnly
	}
	if cfg.BRRDAPOnly != nil {
		s.BRRDAPOnly = *cfg.BRRDAPOnly
	}
	if cfg.HideRegistered != nil {
		s.HideRegistered = *cfg.HideRegistered
	}
	if cfg.IncludeInvalid != nil {
		s.IncludeInvalid = *cfg.IncludeInvalid
	}
	if cfg.ForceRecheck != nil {
		s.ForceRecheck = *cfg.ForceRecheck
	}
	if cfg.ShortFirst != nil {
		s.ShortFirst = *cfg.ShortFirst
	}
	if cfg.WordFirst != nil {
		s.WordFirst = *cfg.WordFirst
	}
	if cfg.ExpiringSoonDays != nil {
		s.ExpiringSoonDays = *cfg.ExpiringSoonDays
	}

	// RDAP (new section + legacy top-level keys).
	if cfg.RDAP.BootstrapURL != nil {
		s.RDAP.BootstrapURL = *cfg.RDAP.BootstrapURL
	} else if cfg.BootstrapURL != nil {
		s.RDAP.BootstrapURL = *cfg.BootstrapURL
	}
	if cfg.RDAP.BootstrapCachePath != nil {
		s.RDAP.BootstrapCachePath = *cfg.RDAP.BootstrapCachePath
	} else if cfg.BootstrapCachePath != nil {
		s.RDAP.BootstrapCachePath = *cfg.BootstrapCachePath
	}
	if cfg.RDAP.BootstrapRefresh != nil {
		s.RDAP.BootstrapRefresh = *cfg.RDAP.BootstrapRefresh
	} else if cfg.BootstrapRefresh != nil {
		s.RDAP.BootstrapRefresh = *cfg.BootstrapRefresh
	}
	if cfg.RDAP.RawOutputDir != nil {
		s.RDAP.RawOutputDir = *cfg.RDAP.RawOutputDir
	} else if cfg.RawOutputDir != nil {
		s.RDAP.RawOutputDir = *cfg.RawOutputDir
	}
	if err = applyDuration(&s.RDAP.RateLimit, cfg.RDAP.RateLimit, "rdap rate_limit"); err != nil {
		return err
	}

	// Store (+ legacy [cache]).
	if cfg.Store.Enabled != nil {
		s.Store.Enabled = *cfg.Store.Enabled
	} else if cfg.Cache.Enabled != nil {
		s.Store.Enabled = *cfg.Cache.Enabled
	}
	if cfg.Store.Path != nil {
		s.Store.Path = *cfg.Store.Path
	} else if cfg.Cache.Path != nil {
		s.Store.Path = *cfg.Cache.Path
	}
	if err = applyDuration(&s.Store.RegisteredTTL, cfg.Store.RegisteredTTL, "store registered_ttl"); err != nil {
		return err
	}
	if err = applyDuration(&s.Store.ReservedTTL, cfg.Store.ReservedTTL, "store reserved_ttl"); err != nil {
		return err
	}

	if cfg.Resume.Path != nil {
		s.Resume.Path = *cfg.Resume.Path
	}
	if cfg.Export.CSV != nil {
		s.Export.CSV = *cfg.Export.CSV
	}
	if cfg.Export.JSON != nil {
		s.Export.JSON = *cfg.Export.JSON
	}

	// Cloudflare.
	if cfg.Cloudflare.AccountID != nil {
		s.Cloudflare.AccountID = *cfg.Cloudflare.AccountID
	}
	if cfg.Cloudflare.APIToken != nil {
		s.Cloudflare.APIToken = *cfg.Cloudflare.APIToken
	}
	if cfg.Cloudflare.APIBaseURL != nil {
		s.Cloudflare.APIBaseURL = *cfg.Cloudflare.APIBaseURL
	}
	if err = applyDuration(&s.Cloudflare.RateLimit, cfg.Cloudflare.RateLimit, "cloudflare rate_limit"); err != nil {
		return err
	}

	// Vercel.
	if cfg.Vercel.APIToken != nil {
		s.Vercel.APIToken = *cfg.Vercel.APIToken
	}
	if cfg.Vercel.APIBaseURL != nil {
		s.Vercel.APIBaseURL = *cfg.Vercel.APIBaseURL
	}
	if cfg.Vercel.TeamID != nil {
		s.Vercel.TeamID = *cfg.Vercel.TeamID
	}
	if err = applyDuration(&s.Vercel.RateLimit, cfg.Vercel.RateLimit, "vercel rate_limit"); err != nil {
		return err
	}
	if cfg.Vercel.FetchPrice != nil {
		s.Vercel.FetchPrice = *cfg.Vercel.FetchPrice
	}
	if cfg.Vercel.PriceYears != nil {
		s.Vercel.PriceYears = *cfg.Vercel.PriceYears
	}
	if cfg.Vercel.Accounts != nil {
		s.Vercel.Accounts = make([]VercelAccount, 0, len(cfg.Vercel.Accounts))
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
			s.Vercel.Accounts = append(s.Vercel.Accounts, item)
		}
	}

	// Hostinger.
	if cfg.Hostinger.APIToken != nil {
		s.Hostinger.APIToken = *cfg.Hostinger.APIToken
	}
	if cfg.Hostinger.APIBaseURL != nil {
		s.Hostinger.APIBaseURL = *cfg.Hostinger.APIBaseURL
	}
	if err = applyDuration(&s.Hostinger.RateLimit, cfg.Hostinger.RateLimit, "hostinger rate_limit"); err != nil {
		return err
	}
	if cfg.Hostinger.Accounts != nil {
		s.Hostinger.Accounts = make([]HostingerAccount, 0, len(cfg.Hostinger.Accounts))
		for _, account := range cfg.Hostinger.Accounts {
			item := HostingerAccount{}
			if account.Name != nil {
				item.Name = *account.Name
			}
			if account.APIToken != nil {
				item.APIToken = *account.APIToken
			}
			s.Hostinger.Accounts = append(s.Hostinger.Accounts, item)
		}
	}

	// API server.
	if cfg.API.Listen != nil {
		s.API.Listen = *cfg.API.Listen
	}
	if cfg.API.AuthToken != nil {
		s.API.AuthToken = *cfg.API.AuthToken
	}

	// Notifications.
	if cfg.Notify.OnFinish != nil {
		s.Notify.OnFinish = *cfg.Notify.OnFinish
	}
	if cfg.Notify.OnAvailable != nil {
		s.Notify.OnAvailable = *cfg.Notify.OnAvailable
	}
	if cfg.Notify.Discord.WebhookURL != nil {
		s.Notify.DiscordWebhook = *cfg.Notify.Discord.WebhookURL
	} else if cfg.Discord.WebhookURL != nil {
		s.Notify.DiscordWebhook = *cfg.Discord.WebhookURL
	}
	if cfg.Notify.Telegram.BotToken != nil {
		s.Notify.TelegramToken = *cfg.Notify.Telegram.BotToken
	} else if cfg.Telegram.BotToken != nil {
		s.Notify.TelegramToken = *cfg.Telegram.BotToken
	}
	if cfg.Notify.Telegram.ChatID != nil {
		s.Notify.TelegramChatID = *cfg.Notify.Telegram.ChatID
	} else if cfg.Telegram.ChatID != nil {
		s.Notify.TelegramChatID = *cfg.Telegram.ChatID
	}
	if cfg.Notify.Telegram.APIBase != nil {
		s.Notify.TelegramAPIBase = *cfg.Notify.Telegram.APIBase
	}
	if cfg.Notify.Slack.WebhookURL != nil {
		s.Notify.SlackWebhook = *cfg.Notify.Slack.WebhookURL
	}
	if cfg.Notify.Webhook.URL != nil {
		s.Notify.WebhookURL = *cfg.Notify.Webhook.URL
	}
	if cfg.Notify.Email.Host != nil {
		s.Notify.Email.Host = *cfg.Notify.Email.Host
	}
	if cfg.Notify.Email.Port != nil {
		s.Notify.Email.Port = *cfg.Notify.Email.Port
	}
	if cfg.Notify.Email.Username != nil {
		s.Notify.Email.Username = *cfg.Notify.Email.Username
	}
	if cfg.Notify.Email.Password != nil {
		s.Notify.Email.Password = *cfg.Notify.Email.Password
	}
	if cfg.Notify.Email.From != nil {
		s.Notify.Email.From = *cfg.Notify.Email.From
	}
	if cfg.Notify.Email.To != nil {
		s.Notify.Email.To = append([]string(nil), cfg.Notify.Email.To...)
	}
	if cfg.Notify.Email.TLSMode != nil {
		s.Notify.Email.TLSMode = *cfg.Notify.Email.TLSMode
	}
	if cfg.Notify.GitHub.Token != nil {
		s.Notify.GitHub.Token = *cfg.Notify.GitHub.Token
	}
	if cfg.Notify.GitHub.Owner != nil {
		s.Notify.GitHub.Owner = *cfg.Notify.GitHub.Owner
	}
	if cfg.Notify.GitHub.Repo != nil {
		s.Notify.GitHub.Repo = *cfg.Notify.GitHub.Repo
	}
	if cfg.Notify.GitHub.APIBase != nil {
		s.Notify.GitHub.APIBase = *cfg.Notify.GitHub.APIBase
	}
	if s.Notify.GitHub.APIBase == "" {
		s.Notify.GitHub.APIBase = defaultGitHubAPIBase
	}

	return nil
}

func applyDuration(target *time.Duration, raw *string, name string) error {
	if raw == nil {
		return nil
	}
	value, err := time.ParseDuration(*raw)
	if err != nil {
		return fmt.Errorf("invalid config %s: %w", name, err)
	}
	*target = value
	return nil
}

// Validate sanity-checks the resolved settings.
func Validate(s Settings) error {
	if s.Concurrency < 1 {
		return errors.New("invalid concurrency: use a value greater than zero (example: -concurrency 8)")
	}
	if s.Timeout <= 0 {
		return errors.New("invalid timeout: use a duration greater than zero (example: -timeout 12s)")
	}
	if s.RateLimit < 0 {
		return errors.New("invalid rate limit: duration cannot be negative (example: -rate-limit 500ms)")
	}
	if s.MaxAttempts < 1 {
		return errors.New("invalid max attempts: use a value of at least 1 (example: max_attempts = 4)")
	}
	if s.NetworkRetries < 0 {
		return errors.New("invalid network retries: value cannot be negative (example: network_retries = 3)")
	}
	if s.SourceFreeze < 0 {
		return errors.New("invalid source freeze: duration cannot be negative (example: source_freeze = \"15m\")")
	}
	if s.SourceMaxDelay < 0 {
		return errors.New("invalid source rate limit max delay: duration cannot be negative")
	}
	if s.ProgressInterval <= 0 {
		return errors.New("invalid progress interval: use a duration greater than zero (example: progress_interval = \"15s\")")
	}
	if s.UserAgent == "" {
		return errors.New("user agent missing: set user_agent in qda.toml or pass -user-agent")
	}
	if len(s.TLDs) == 0 {
		return errors.New("no TLDs configured: add tlds to qda.toml or pass -tld net,org,com")
	}
	if s.ExpiringSoonDays < 0 {
		return errors.New("invalid expiring_soon_days: value cannot be negative")
	}
	return nil
}
