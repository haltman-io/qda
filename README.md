# qda

```text
                      ░▓▓█▌
    ▄▄▄▄▄         ▄▄▄▄░▒▓▓▌      ▄░░▄▄
 ▄▄▄▓▓▀▒▓▓ ▄   ▄ ▓▓▒▀▓▓▓▓▓▌   ▄ ░░▒▀▓▓▄▄▄
▐▓▓▓▀▀  ▀█░█▌ ▐█░█▀   ▀▓▓▓▌   ░░█▀  ▀▀▓▓▌▌
▓▓▓▌▌ ░░░▐███ ███▌░░░ █▐▓▓▓ ▐   ▌░░░ ▐▐▓▓▌
▐▀▓▀ ▄  ▄ ░░█ ▐█▓ ▄    ▀▓▀▓ ▐▓▓      ▐▀▓▌▌
 ▀  ░░   ░▒▒░  ▀░░   ░░  ██  ▀█░░ ▄▄▄░ ░░░
    ▀▀░▀▀█░▓░     ▀▀░▀▀ ▀▀      ▀▀▀▀░▒▒▀▀▀
         █▒█▒

   quick domain availability
```

**Massive multi-source domain availability recon** — RDAP-first, streaming output, resume, local DB, notifications, and HTTP API.

Inspired by [ProjectDiscovery](https://projectdiscovery.io) tools (`httpx`, `nuclei`, `subfinder`).

[![Go](https://img.shields.io/badge/Go-1.24+-00ADD8?style=flat-square&logo=go&logoColor=white)](https://go.dev/)
[![License](https://img.shields.io/badge/license-Unlicense-blue?style=flat-square)](LICENSE)
[![Version](https://img.shields.io/badge/version-0.2.0-red?style=flat-square)](https://github.com/haltman-io/qda)
[![haltman.io](https://img.shields.io/badge/haltman.io-black?style=flat-square)](https://haltman.io)

---

## Table of contents

- [Features](#features)
- [Install](#install)
- [Quick start](#quick-start)
- [Commands](#commands)
- [How it works](#how-it-works)
- [Configuration](#configuration)
- [HTTP API](#http-api)
- [Output](#output)
- [Project layout](#project-layout)
- [License](#license)

---

## Features

| Area | What you get |
|------|----------------|
| **Checks** | RDAP-first via IANA bootstrap; optional Cloudflare → Vercel → Hostinger fallback |
| **Scale** | Per-registry rate limits, freezes, requeue-to-end, proxy rotation |
| **State** | Local JSON DB with TTL reuse; resume after `Ctrl+C` |
| **UX** | Streaming colored lines, progress logs, silent/verbose/debug |
| **Input** | Words × TLD expansion, prioritized groups, stdin, on-demand generators |
| **Export** | Console, JSONL, JSON, CSV |
| **Ops** | Discord / Telegram / Slack / webhook / email / GitHub issues |
| **API** | `qda api` for live checks and DB queries |

---

## Install

**Requirements:** Go 1.24+

```bash
git clone https://github.com/haltman-io/qda.git
cd qda
go build -o qda ./cmd/qda
```

Or run tests + build:

```bash
go test ./...
go build -o qda ./cmd/qda
```

---

## Quick start

```bash
# sample config (won't overwrite an existing file)
./qda init-config -o qda.toml

# wordlist × TLDs from config (auto-loads ./qda.toml)
./qda run words.txt

# single domain / word expansion
./qda run -d bug.net
./qda run -d kernel -tld net,org,com

# stdin
cat words.txt | ./qda run -tld net,org

# resume after interrupt
./qda run -resume

# local DB
./qda db -available
./qda db -expiring-in 30

# HTTP API
./qda api -listen 127.0.0.1:8080

# generate combinations
./qda generate -len 3 -charset alnum -o combos.txt
```

> Legacy: `qda words.txt -config qda.toml` still works (routes to `run`).

---

## Commands

| Command | Aliases | Description |
|---------|---------|-------------|
| `run` | `scan` | Mass availability scan |
| `api` | `server`, `serve` | HTTP API server |
| `db` | `query` | Query the local results database |
| `generate` | `gen` | Build wordlists / domain lists |
| `init-config` | — | Write a sample `qda.toml` |
| `version` | — | Print version |
| `help` | — | Root help |

```bash
qda <command> -h
```

### `run` highlights

```bash
./qda run words.txt -concurrency 16 -fallback
./qda run -d short -tld-group best,medium
./qda run words.txt -jsonl -o hits.txt -hide-registered
./qda run words.txt -silent -nc          # results only, no color
./qda run words.txt -resume              # continue last scan
./qda run words.txt -force               # ignore DB cache
```

| Flag | Purpose |
|------|---------|
| `-d` / `-domain` | Domain or word (repeatable / comma-separated) |
| `-l` / `-list` | Input file (else positional or stdin) |
| `-tld` | TLDs for word expansion |
| `-tld-group` | `best`, `medium`, `common` (configurable) |
| `-fallback` | Enable registrar chain |
| `-resume` | Resume from state file |
| `-force` | Bypass local DB |
| `-silent` / `-v` / `-debug` | Verbosity |
| `-nc` | Disable colors |
| `-jsonl` / `-json` / `-csv` / `-o` | Exports |
| `-no-notify` / `-no-progress` / `-no-state` | Disable extras |

### `db`

```bash
./qda db -stats
./qda db -available
./qda db -status registered -tld net
./qda db -expiring-in 30
./qda db -q kernel -json out.json
```

### `generate`

```bash
./qda generate -len 3 -charset alnum -o combos.txt
./qda generate -min 1 -max 2 -charset letters -o shorts.txt
./qda generate -merge a.txt,b.txt -o merged.txt
./qda generate -merge words.txt -tlds net,org,com -o domains.txt
```

---

## How it works

```text
input → normalize → DB reuse → RDAP → [fallback chain] → stream + store + notify
```

1. **Normalize** — words vs FQDNs, IDNA, Public Suffix List  
2. **Cache** — reuse fresh `registered` / `reserved` only; everything else is live  
3. **RDAP** — authoritative registry from IANA DNS bootstrap (`404` ≈ available)  
4. **Fallback** — when enabled: Cloudflare → Vercel → Hostinger  
5. **Rate limits** — freeze `rdap:{host}` or `source:{name}`, requeue to end  
6. **Emit** — one line per domain immediately; persist store + resume state  

### Status model

| Status | Console | Meaning |
|--------|---------|---------|
| `available` | `AVAILABLE` | Free / registrable |
| `premium` | `PREMIUM` | Registrable at premium pricing |
| `registered` | `REGISTERED` | Taken |
| `reserved` | `RESERVED` | Policy-blocked / not registrable |
| `redemption` / `pending_delete` | `SOON` | Near end-of-life windows |
| `rate_limited` | `RATE-LIMITED` | Upstream freeze + retry |
| `unknown` | `UNKNOWN` | No definitive answer |
| `invalid` | `INVALID` | Bad input |

### Registro.br / `.br`

With `br_rdap_only = true` (default), `.br` stays on Registro.br RDAP even when global fallback is on.  
`Nicbr-Rate-Limit-Exceeded` / `Nicbr-Permission-Denied` freeze the RDAP key and are **not** sent to registrar APIs.  
A `404` counts as available only when those headers are absent.

### TLD priority

| Group | Default TLDs |
|-------|----------------|
| **best** | `net`, `org`, `com` |
| **medium** | `io`, `to`, `lat`, `sh` |
| **common** | `is`, `me`, `my`, `lol`, `nl`, `ph`, `gg`, `cc`, `eu`, `in`, `id`, `ch`, `ws` |

```bash
./qda run words.txt -tld-group best,medium
./qda run words.txt -tld net,org,lat
```

Default order is TLD-major (`word_first = false`). Use `-short-first` / `short_first` to prefer shorter names.

### Resume

On `SIGINT` / `SIGTERM`, pending + in-flight work is snapshotted. Input is hashed so a mismatched wordlist won't resume silently.

```bash
./qda run -resume
./qda run words.txt -resume
```

---

## Configuration

Auto-loaded from `./qda.toml` when present. **CLI flags always win.**

```bash
./qda init-config -o qda.toml
```

```toml
tlds = ["net", "org", "com", "io", "to", "lat", "sh"]
concurrency = 8
rate_limit = "500ms"
max_attempts = 4
rdap_only = true          # false or -fallback → registrar chain
br_rdap_only = true

# proxies = ["http://localhost:8100"]
# proxy_file = "proxies.txt"

[rdap]
bootstrap_url = "https://data.iana.org/rdap/dns.json"
bootstrap_refresh = true

[store]
enabled = true
path = ".qda-cache/qda.db.json"
registered_ttl = "168h"
reserved_ttl = "720h"

[resume]
path = ".qda-cache/state.json"

[cloudflare]
account_id = ""
api_token = ""

[vercel]
api_token = ""
# [[vercel.accounts]]   # optional rotation

[hostinger]
api_token = ""
# [[hostinger.accounts]]

[notify]
on_finish = true
on_available = false

[notify.discord]
webhook_url = ""

[notify.telegram]
bot_token = ""
chat_id = ""

[api]
listen = "127.0.0.1:8080"
auth_token = ""
```

**Legacy keys still load:** `[cache]` → `[store]`, `[discord]` / `[telegram]` → `[notify.*]`, top-level `bootstrap_*`, `raw_output_dir`, `source_rate_limit_retries`.

### Proxies

```toml
proxies = ["http://localhost:8100"]
# or
proxy_file = "proxies.txt"
```

### Notifications

Any combination of:

- Discord / Slack / generic JSON webhooks  
- Telegram Bot API  
- SMTP email (`starttls` / `tls` / `none`)  
- GitHub issues (on finish)  

`on_finish` → summary · `on_available` → per interesting hit.

---

## HTTP API

```bash
./qda api -listen 127.0.0.1:8080
# auth: [api].auth_token or -auth-token
```

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/health` | Liveness (no auth) |
| `GET` | `/v1/stats` | DB aggregates |
| `GET` | `/v1/domains` | Query stored domains |
| `GET` | `/v1/domains/{domain}` | One stored domain |
| `GET` | `/v1/check?domain=x` | Live check (`force=true` skips cache) |
| `POST` | `/v1/check` | Batch check (JSON body, max 1000) |

Auth (when set): `X-API-Key` or `Authorization: Bearer <token>`.

```bash
curl -s 'http://127.0.0.1:8080/v1/check?domain=example.net'
curl -s -X POST http://127.0.0.1:8080/v1/check \
  -H 'Content-Type: application/json' \
  -d '{"domains":["a.net","b.org"],"words":["kernel"],"tlds":["net"]}'
```

---

## Output

Streaming (one domain at a time):

```text
[AVAILABLE] free.net [rdap] 142ms
[REGISTERED] taken.org expires=2027-01-12 [rdap] 98ms
[PREMIUM] short.io [vercel] 210ms
[RATE-LIMITED] foo.br freeze=15m [rdap]
```

Final summary prioritizes available / premium / soon-to-expire.

```bash
./qda run words.txt -jsonl results.jsonl -o hits.txt
# or [export] csv/json in qda.toml
```

---

## Project layout

```text
cmd/qda/              entrypoint
internal/cli/         subcommands + flags
internal/runner/      engine, queue, mass scan
internal/sources/     rdap, cloudflare, vercel, hostinger
internal/store/       local JSON DB
internal/resume/      interrupt / resume state
internal/ratelimit/   keyed pacing + freezes
internal/httpkit/     client, proxy rotation, retries
internal/output/      colored streaming + exports
internal/notify/      notification channels
internal/api/         HTTP API
internal/generate/    wordlist generation
internal/domainx/     normalization + expansion
internal/config/      TOML + defaults + legacy compat
internal/banner/      ASCII / ANSI banner
```

```bash
go test ./...
go build -o qda ./cmd/qda
./qda run wl-top-10.txt -tld net -silent
```

---

## License

[Unlicense](LICENSE) — public domain.

---

<p align="center">
  <a href="https://haltman.io">haltman.io</a>
  ·
  <a href="https://github.com/haltman-io/qda">github.com/haltman-io/qda</a>
</p>
