// Command gatherdeal is the GatherDeal command-line interface.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/gatherdeal/cli/internal/cli"
	"github.com/gatherdeal/cli/internal/store"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	env := &cli.Env{
		Stdin:       os.Stdin,
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
		Getenv:      os.Getenv,
		Interactive: cli.IsTerminal(os.Stdin) && cli.IsTerminal(os.Stderr),
		Keyring:     store.OSKeyring{},
		Hostname:    os.Hostname,
	}
	code := cli.Run(ctx, env, os.Args[1:])
	stop()
	os.Exit(code)
}
