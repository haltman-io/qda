package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"qda/internal/version"
)

func initConfigCmd(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("qda init-config", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var outputPath string
	fs.StringVar(&outputPath, "o", "qda.toml", "Destination path")
	fs.StringVar(&outputPath, "output", "qda.toml", "Alias for -o")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Write a sample qda.toml configuration. Refuses to overwrite existing files.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		outputPath = fs.Arg(0)
	}
	if strings.TrimSpace(outputPath) == "" {
		fmt.Fprintln(stderr, "[ERR] config path is required")
		return 2
	}

	if dir := filepath.Dir(outputPath); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintf(stderr, "[ERR] %v\n", err)
			return 1
		}
	}
	file, err := os.OpenFile(outputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			fmt.Fprintf(stderr, "[ERR] config file already exists: %s (refusing to overwrite)\n", outputPath)
			return 1
		}
		fmt.Fprintf(stderr, "[ERR] %v\n", err)
		return 1
	}
	defer file.Close()
	if _, err := file.WriteString(sampleConfig); err != nil {
		fmt.Fprintf(stderr, "[ERR] %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Created config file: %s\n", outputPath)
	fmt.Fprintln(stdout, "Next: qda run words.txt -config "+outputPath)
	return 0
}

const sampleConfig = `# qda configuration — v` + version.Version + `
# CLI flags override these values without rewriting this file.

# TLDs in priority order. Words are expanded across these suffixes; the
# first TLD class is fully checked before moving to the next one.
tlds = ["net", "org", "com", "io", "to", "lat", "sh"]

# Named TLD groups usable via: qda run -tld-group best,medium
[tld_groups]
best   = ["net", "org", "com"]
medium = ["io", "to", "lat", "sh"]
common = ["is", "me", "my", "lol", "nl", "ph", "gg", "cc", "eu", "in", "id", "ch", "ws"]

concurrency = 8            # concurrent checks
timeout = "12s"            # per-request HTTP timeout
rate_limit = "500ms"       # minimum delay between requests per source key
max_attempts = 4           # per-domain attempts before giving up
network_retries = 3        # transient network retries inside one attempt
network_retry_delay = "2s"
source_freeze = "15m"      # freeze a rate-limited/blocked source for this long
# 0s disables the cap, so upstream Retry-After is respected as sent.
source_rate_limit_max_delay = "0s"
progress_interval = "15s"  # progress line cadence
show_progress = true       # false or -no-progress: keep results/logs, hide periodic progress
user_agent = "qda/` + version.Version + ` (+mailto:you@example.com)"

# RDAP is authoritative by default. Set rdap_only = false (or pass -fallback)
# to confirm unknowns through cloudflare, vercel and hostinger.
rdap_only = true
br_rdap_only = true        # keep .br exclusive to Registro.br RDAP
hide_registered_reserved = false
include_invalid = false
force_recheck = false
short_first = false        # check shortest words first
word_first = false         # iterate words first instead of TLD classes first
expiring_soon_days = 30

# Optional HTTP/HTTPS proxy rotation (e.g. proton-privoxy on port 8100).
proxies = []
# proxies = ["http://localhost:8100"]
proxy_file = ""

[rdap]
bootstrap_url = "https://data.iana.org/rdap/dns.json"
bootstrap_cache_path = ".qda-cache/rdap-dns.json"
bootstrap_refresh = true
raw_output_dir = ""        # set to ".qda-cache/rdap-raw" to dump raw RDAP
rate_limit = ""            # per-registry override (e.g. "2s"); empty uses rate_limit

[store]                    # local results database (qda db / API)
enabled = true
path = ".qda-cache/qda.db.json"
registered_ttl = "168h"    # reuse registered results for up to 7 days
reserved_ttl = "720h"      # reuse reserved results for up to 30 days

[resume]
path = ".qda-cache/state.json"

[export]
csv = ""                   # e.g. ".qda-output/results.csv"
json = ""                  # e.g. ".qda-output/results.json"

[cloudflare]
account_id = ""
api_token = ""
api_base_url = "https://api.cloudflare.com/client/v4"
rate_limit = "2s"

[vercel]
api_token = ""
api_base_url = "https://api.vercel.com"
team_id = ""
rate_limit = "1s"
fetch_price = false
price_years = ""

# Key rotation: add as many accounts as you like.
# [[vercel.accounts]]
# name = "vercel-2"
# api_token = ""
# team_id = ""

[hostinger]
api_token = ""
api_base_url = "https://developers.hostinger.com"
rate_limit = "6s"

# [[hostinger.accounts]]
# name = "hostinger-2"
# api_token = ""

[api]
listen = "127.0.0.1:8080"
auth_token = ""            # require Bearer/X-API-Key when set

[notify]
on_finish = true
on_available = false       # notify immediately when a domain is available

[notify.discord]
webhook_url = ""

[notify.telegram]
bot_token = ""
chat_id = ""

[notify.slack]
webhook_url = ""

[notify.webhook]           # generic JSON POST with the full payload
url = ""

[notify.email]
host = ""                  # e.g. "smtp.gmail.com"
port = 587
username = ""
password = ""
from = ""
to = []
tls = "starttls"           # starttls | tls | none

[notify.github]            # opens an issue when the scan finishes
token = ""
owner = ""
repo = ""
`
