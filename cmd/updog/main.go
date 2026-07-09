package main

import (
	"os"

	"github.com/wuzupdog/updog_cli/internal/cli"
)

var version = "dev"

func main() {
	os.Exit(cli.Run(cli.Options{Version: version}))
}
