package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/wuzupdog/updog_cli/internal/cli"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(cli.Run(cli.Options{Version: version, Context: ctx}))
}
