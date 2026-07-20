package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The user's real qda.toml mixes new keys with legacy keys ([cache],
// [discord], [telegram], top-level bootstrap_* and raw_output_dir,
// source_rate_limit_retries, tui). It must keep loading.
const legacyTOML = `
tlds = ["com", "net", "org"]
concurrency = 6
timeout = "12s"
rate_limit = "2s"
source_rate_limit_retries = 3
source_rate_limit_max_delay = "0s"
network_retries = 3
network_retry_delay = "2s"
user_agent = "qda/0.1.2 (+mailto:e@netsrc.org)"
tui = false
include_invalid = false
force_recheck = false
rdap_only = false
br_rdap_only = true
hide_registered_reserved = false
expiring_soon_days = 30
bootstrap_url = "https://data.iana.org/rdap/dns.json"
bootstrap_cache_path = ".qda-cache/rdap-dns.json"
bootstrap_refresh = true
proxies = []
proxy_file = ""
raw_output_dir = ".qda-cache/rdap-raw"

[cache]
enabled = true
path = ".qda-cache/results.json"

[cloudflare]
account_id = "acc-1"
api_token = "cf-token"
api_base_url = "https://api.cloudflare.com/client/v4"
batch_size = 20

[vercel]
api_token = "vc-token"
api_base_url = "https://api.vercel.com"
team_id = "team-1"
batch_size = 50
rate_limit = "1s"
fetch_price = false
price_years = ""

[[vercel.accounts]]
name = "vercel-2"
api_token = "vc-token-2"
team_id = "team-2"

[hostinger]
api_token = "h-token"
api_base_url = "https://developers.hostinger.com"
rate_limit = "6s"

[[hostinger.accounts]]
name = "hostinger-2"
api_token = "h-token-2"

[export]
csv = ".qda-output/results.csv"
json = ".qda-output/results.json"

[discord]
webhook_url = "https://discord.test/webhook"

[telegram]
bot_token = "tg-token"
chat_id = "chat-1"
`

func TestLegacyConfigLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qda.toml")
	if err := os.WriteFile(path, []byte(legacyTOML), 0o644); err != nil {
		t.Fatal(err)
	}

	settings := Default()
	if err := LoadFile(path, &settings); err != nil {
		t.Fatal(err)
	}

	if settings.Concurrency != 6 {
		t.Fatalf("concurrency = %d", settings.Concurrency)
	}
	if settings.RateLimit != 2*time.Second {
		t.Fatalf("rate limit = %s", settings.RateLimit)
	}
	if settings.MaxAttempts != 4 {
		t.Fatalf("max attempts = %d (legacy source_rate_limit_retries=3 should map)", settings.MaxAttempts)
	}
	if settings.RDAPOnly {
		t.Fatal("rdap_only should be false")
	}
	if !settings.BRRDAPOnly {
		t.Fatal("br_rdap_only should be true")
	}
	if settings.Store.Path != ".qda-cache/results.json" {
		t.Fatalf("legacy [cache] path should map to store, got %q", settings.Store.Path)
	}
	if settings.RDAP.RawOutputDir != ".qda-cache/rdap-raw" {
		t.Fatalf("legacy raw_output_dir should map, got %q", settings.RDAP.RawOutputDir)
	}
	if settings.RDAP.BootstrapCachePath != ".qda-cache/rdap-dns.json" {
		t.Fatalf("legacy bootstrap cache path should map, got %q", settings.RDAP.BootstrapCachePath)
	}
	if settings.Cloudflare.APIToken != "cf-token" || settings.Cloudflare.AccountID != "acc-1" {
		t.Fatal("cloudflare credentials not loaded")
	}
	if len(settings.Vercel.Accounts) != 1 || settings.Vercel.Accounts[0].APIToken != "vc-token-2" {
		t.Fatalf("vercel accounts = %+v", settings.Vercel.Accounts)
	}
	if len(settings.Hostinger.Accounts) != 1 || settings.Hostinger.Accounts[0].APIToken != "h-token-2" {
		t.Fatalf("hostinger accounts = %+v", settings.Hostinger.Accounts)
	}
	if settings.Notify.DiscordWebhook != "https://discord.test/webhook" {
		t.Fatal("legacy [discord] should map to notify")
	}
	if settings.Notify.TelegramToken != "tg-token" || settings.Notify.TelegramChatID != "chat-1" {
		t.Fatal("legacy [telegram] should map to notify")
	}
	if settings.Export.CSV != ".qda-output/results.csv" {
		t.Fatal("export csv not loaded")
	}
}

func TestNewConfigSections(t *testing.T) {
	tomlDoc := `
tlds = ["net", "org"]
[tld_groups]
best = ["net", "org", "com"]

[store]
enabled = true
path = "custom/db.json"
registered_ttl = "48h"
reserved_ttl = "24h"

[resume]
path = "custom/state.json"

[api]
listen = "0.0.0.0:9000"
auth_token = "secret"

[notify]
on_finish = false
on_available = true

[notify.email]
host = "smtp.test"
port = 465
from = "a@test"
to = ["b@test"]
tls = "tls"

[notify.github]
token = "gh"
owner = "me"
repo = "alerts"

[rdap]
rate_limit = "3s"
raw_output_dir = "raw"
`
	path := filepath.Join(t.TempDir(), "qda.toml")
	if err := os.WriteFile(path, []byte(tomlDoc), 0o644); err != nil {
		t.Fatal(err)
	}
	settings := Default()
	if err := LoadFile(path, &settings); err != nil {
		t.Fatal(err)
	}

	if settings.TLDGroups["best"][0] != "net" {
		t.Fatalf("tld groups = %+v", settings.TLDGroups)
	}
	if settings.Store.Path != "custom/db.json" || settings.Store.RegisteredTTL != 48*time.Hour {
		t.Fatalf("store = %+v", settings.Store)
	}
	if settings.Resume.Path != "custom/state.json" {
		t.Fatalf("resume = %+v", settings.Resume)
	}
	if settings.API.Listen != "0.0.0.0:9000" || settings.API.AuthToken != "secret" {
		t.Fatalf("api = %+v", settings.API)
	}
	if settings.Notify.OnFinish || !settings.Notify.OnAvailable {
		t.Fatalf("notify = %+v", settings.Notify)
	}
	if settings.Notify.Email.Host != "smtp.test" || settings.Notify.Email.Port != 465 || settings.Notify.Email.TLSMode != "tls" {
		t.Fatalf("email = %+v", settings.Notify.Email)
	}
	if settings.Notify.GitHub.Repo != "alerts" {
		t.Fatalf("github = %+v", settings.Notify.GitHub)
	}
	if settings.RDAP.RateLimit != 3*time.Second || settings.RDAP.RawOutputDir != "raw" {
		t.Fatalf("rdap = %+v", settings.RDAP)
	}
}

func TestValidate(t *testing.T) {
	settings := Default()
	if err := Validate(settings); err != nil {
		t.Fatalf("default settings should validate: %v", err)
	}
	settings.Concurrency = 0
	if err := Validate(settings); err == nil {
		t.Fatal("expected validation error for concurrency 0")
	}
	settings = Default()
	settings.TLDs = nil
	if err := Validate(settings); err == nil {
		t.Fatal("expected validation error for empty TLDs")
	}
}
