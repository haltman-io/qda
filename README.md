# qda

`qda` means Quick Domain Availability because I was bored of trying to find specific domains on namecheap.

read the code to understand how this shit works, your little b1tch! >:P

## Features

- RDAP-first checks through the IANA RDAP bootstrap.
- Public Suffix List handling for registrable domains such as `example.com.br`.
- IDNA/punycode normalization for Unicode domains.
- Conservative status model: `available`, `registered`, `redemption`,
  `pending_delete`, `rate_limited`, `unknown`, `invalid`.
- RDAP pre-check skips already registered domains before Cloudflare calls.
- Cloudflare Registrar domain-check source for authoritative registrability.
- Vercel bulk availability fallback for domains Cloudflare leaves unknown.
- Hostinger availability fallback for domains Cloudflare and Vercel leave unknown.
- Per-source rate-limit backoff and retry for Cloudflare, Vercel, and Hostinger.
- Maximum per-source `Retry-After` cap so one API cannot freeze the run.
- Local result cache enabled by default for registered domains.
- Console filter to hide `REGISTERED` and `RESERVED` domains.
- TOML configuration with CLI flag overrides.
- Colored per-domain logging by default, with `--tui` available if needed.
- Concurrent checks with rate limiting and timeout control.
- HTTP/HTTPS proxy rotation.
- Optional raw RDAP JSON persistence for audit/debug.
- CSV and JSON export.
- Discord and Telegram completion notifications.
- Final output is a prioritized table: available and premium first, available
  soon next, registered last.

## Install After Clone

```bash
go mod download
go test ./...
go build -o qda ./cmd/qda
```

## How I used this:

```bash
./qda Retard4dHostnam3sCand1dates.txt -config myFuck1ngConfiguration.toml
```