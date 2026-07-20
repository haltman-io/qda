package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"qda/internal/api"
	"qda/internal/banner"
	"qda/internal/config"
	"qda/internal/output"
	"qda/internal/runner"
	"qda/internal/store"
	"qda/internal/version"
)

func apiCmd(args []string, stdout io.Writer, stderr io.Writer) int {
	settings := config.Default()

	fs := flag.NewFlagSet("qda api", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var configPath, listen, authToken string
	var silent bool
	fs.StringVar(&configPath, "config", "", "Path to a TOML configuration file (default: ./qda.toml when present)")
	fs.StringVar(&listen, "listen", "", "Listen address (overrides [api] listen)")
	fs.StringVar(&authToken, "auth-token", "", "Require this bearer token / X-API-Key for requests")
	fs.BoolVar(&silent, "silent", false, "Only print errors")
	fs.Usage = func() {
		fmt.Fprintf(stderr, `Start the qda HTTP API server.

USAGE
  qda api -listen 127.0.0.1:8080

ENDPOINTS
  GET  /health                      Liveness probe
  GET  /v1/stats                    Database statistics
  GET  /v1/domains                  Query records (?status=&tld=&q=&expiring_in=&available=&limit=)
  GET  /v1/domains/{domain}         Single record
  GET  /v1/check?domain=x.com       Live check one domain (&force=true to bypass the database)
  POST /v1/check                    Live check batch: {"domains":[...],"words":[...],"tlds":[...]}

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
	if listen != "" {
		settings.API.Listen = listen
	}
	if authToken != "" {
		settings.API.AuthToken = authToken
	}
	if err := config.Validate(settings); err != nil {
		fmt.Fprintf(stderr, "[ERR] %v\n", err)
		return 1
	}

	level := output.LevelNormal
	if silent {
		level = output.LevelSilent
	}
	stdoutFile, _ := stdout.(*os.File)
	colors := output.ColorsEnabled(false, stdoutFile)
	printer := output.New(stdout, stderr, level, colors)
	if !silent {
		printer.Raw(banner.Text(colors))
		printer.Infof("current qda version v%s", version.Version)
	}

	db, err := store.Open(settings.Store.Path, settings.Store.Enabled, settings.Store.RegisteredTTL, settings.Store.ReservedTTL)
	if err != nil {
		fmt.Fprintf(stderr, "[ERR] %v\n", err)
		return 1
	}
	if db.Warning() != "" {
		printer.Warnf("%s", db.Warning())
	}

	engine, err := runner.NewEngine(settings, db, printer)
	if err != nil {
		fmt.Fprintf(stderr, "[ERR] %v\n", err)
		return 1
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

	if settings.API.AuthToken == "" {
		printer.Warnf("api auth token is empty; the server is unauthenticated (set [api] auth_token)")
	}

	server := api.NewServer(engine, db, printer, settings.API.AuthToken, settings.ExpiringSoonDays)
	if err := server.ListenAndServe(ctx, settings.API.Listen); err != nil {
		fmt.Fprintf(stderr, "[ERR] %v\n", err)
		return 1
	}
	runner.SaveStore(db, printer)
	return 0
}
