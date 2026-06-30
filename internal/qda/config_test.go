package qda

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseCLIFlagsOverrideConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "qda.toml")
	config := `
tlds = ["com", "net"]
concurrency = 2
timeout = "5s"
source_rate_limit_retries = 5
source_rate_limit_max_delay = "2s"
hide_registered_reserved = true

[cloudflare]
account_id = "account"
api_token = "token"

[hostinger]
api_token = "hostinger"
rate_limit = "5s"

[[hostinger.accounts]]
name = "hostinger-2"
api_token = "hostinger-2-token"

[vercel]
api_token = "vercel"
team_id = "team_123"
rate_limit = "750ms"
fetch_price = true

[[vercel.accounts]]
name = "vercel-2"
api_token = "vercel-2-token"
team_id = "team_456"

[export]
csv = "from-config.csv"
`
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	action, settings, err := ParseCLI([]string{
		"--config", configPath,
		"--concurrency", "9",
		"--tld", "dev",
		"words.txt",
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if action != ActionRun {
		t.Fatalf("got action %v", action)
	}
	if settings.Concurrency != 9 {
		t.Fatalf("got concurrency %d", settings.Concurrency)
	}
	if len(settings.TLDs) != 1 || settings.TLDs[0] != "dev" {
		t.Fatalf("got TLDs %#v", settings.TLDs)
	}
	if settings.CSVOutput != "from-config.csv" {
		t.Fatalf("config CSV was not preserved: %q", settings.CSVOutput)
	}
	if settings.HostingerAPIToken != "hostinger" {
		t.Fatalf("got hostinger token %q", settings.HostingerAPIToken)
	}
	if settings.HostingerRateLimit.String() != "5s" {
		t.Fatalf("got hostinger rate limit %s", settings.HostingerRateLimit)
	}
	if len(settings.HostingerAccounts) != 1 {
		t.Fatalf("got hostinger accounts %#v", settings.HostingerAccounts)
	}
	if settings.HostingerAccounts[0].Name != "hostinger-2" || settings.HostingerAccounts[0].APIToken != "hostinger-2-token" {
		t.Fatalf("unexpected hostinger account: %#v", settings.HostingerAccounts[0])
	}
	if settings.SourceRateLimitRetries != 5 {
		t.Fatalf("got source rate limit retries %d", settings.SourceRateLimitRetries)
	}
	if settings.SourceRateLimitMaxDelay.String() != "2s" {
		t.Fatalf("got source rate limit max delay %s", settings.SourceRateLimitMaxDelay)
	}
	if !settings.HideRegisteredReserved {
		t.Fatal("expected hide registered/reserved from config")
	}
	if settings.VercelAPIToken != "vercel" {
		t.Fatalf("got vercel token %q", settings.VercelAPIToken)
	}
	if settings.VercelTeamID != "team_123" {
		t.Fatalf("got vercel team id %q", settings.VercelTeamID)
	}
	if settings.VercelRateLimit.String() != "750ms" {
		t.Fatalf("got vercel rate limit %s", settings.VercelRateLimit)
	}
	if !settings.VercelFetchPrice {
		t.Fatal("expected vercel fetch price from config")
	}
	if len(settings.VercelAccounts) != 1 {
		t.Fatalf("got vercel accounts %#v", settings.VercelAccounts)
	}
	if settings.VercelAccounts[0].Name != "vercel-2" || settings.VercelAccounts[0].APIToken != "vercel-2-token" || settings.VercelAccounts[0].TeamID != "team_456" {
		t.Fatalf("unexpected vercel account: %#v", settings.VercelAccounts[0])
	}
}

func TestParseCLIAllowsFlagsAfterInput(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "qda.toml")
	config := `
[cloudflare]
account_id = "account"
api_token = "token"
`
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	action, settings, err := ParseCLI([]string{
		"a.txt",
		"-config", configPath,
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if action != ActionRun {
		t.Fatalf("got action %v", action)
	}
	if settings.InputPath != "a.txt" {
		t.Fatalf("got input path %q", settings.InputPath)
	}
	if settings.ConfigPath != configPath {
		t.Fatalf("got config path %q", settings.ConfigPath)
	}
}

func TestParseCLIAllowsInterspersedFlagsWithValues(t *testing.T) {
	action, settings, err := ParseCLI([]string{
		"words.txt",
		"--cloudflare-account-id=account",
		"--tld", "dev",
		"--cloudflare-api-token", "token",
		"--tui",
		"--concurrency", "3",
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if action != ActionRun {
		t.Fatalf("got action %v", action)
	}
	if settings.InputPath != "words.txt" {
		t.Fatalf("got input path %q", settings.InputPath)
	}
	if settings.Concurrency != 3 {
		t.Fatalf("got concurrency %d", settings.Concurrency)
	}
	if len(settings.TLDs) != 1 || settings.TLDs[0] != "dev" {
		t.Fatalf("got TLDs %#v", settings.TLDs)
	}
	if !settings.TUI {
		t.Fatal("expected --tui to enable TUI")
	}
}

func TestParseCLIHelpHasBannerAndExamples(t *testing.T) {
	var stderr bytes.Buffer
	_, _, err := ParseCLI([]string{"--help"}, &stderr)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("got error %v", err)
	}

	text := stderr.String()
	for _, want := range []string{
		"QDA - Quick Domain Availability",
		"USAGE",
		"FAST PATH",
		"qda a.txt -config qda.toml",
		"CONFIG REQUIRED FOR LIVE CHECKS",
		"[vercel]",
		"[hostinger]",
		"Flags may be placed before or after the input file",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in help:\n%s", want, text)
		}
	}
}

func TestParseCLIMissingInputErrorIsActionable(t *testing.T) {
	_, _, err := ParseCLI([]string{
		"--cloudflare-account-id", "account",
		"--cloudflare-api-token", "token",
	}, io.Discard)
	if err == nil {
		t.Fatal("expected error")
	}

	message := err.Error()
	for _, want := range []string{
		"input file missing",
		"qda a.txt -config qda.toml",
		"qda --help",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("missing %q in error %q", want, message)
		}
	}
}

func TestValidateSettingsCloudflareErrorsAreActionable(t *testing.T) {
	settings := DefaultSettings()
	settings.InputPath = "a.txt"
	err := validateSettings(settings)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "[cloudflare] account_id") {
		t.Fatalf("error is not actionable: %q", err.Error())
	}
}

func TestWriteSampleConfigDoesNotOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qda.toml")
	if err := WriteSampleConfig(path); err != nil {
		t.Fatal(err)
	}
	err := WriteSampleConfig(path)
	if err == nil {
		t.Fatal("expected overwrite protection error")
	}
	if !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("unexpected error: %q", err.Error())
	}
}
