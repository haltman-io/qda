package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"qda/internal/qda"
)

func main() {
	action, settings, err := qda.ParseCLI(os.Args[1:], os.Stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	switch action {
	case qda.ActionInitConfig:
		if err := qda.WriteSampleConfig(settings.InitConfigPath); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stdout, "Created config file: %s\n", settings.InitConfigPath)
		fmt.Fprintln(os.Stdout, "Next: fill [cloudflare] account_id and api_token, then run:")
		fmt.Fprintf(os.Stdout, "  qda a.txt -config %s\n", settings.InitConfigPath)
	case qda.ActionVersion:
		fmt.Fprintf(os.Stdout, "qda %s\n", qda.Version)
	case qda.ActionRun:
		if err := qda.Execute(context.Background(), settings, os.Stdout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}
