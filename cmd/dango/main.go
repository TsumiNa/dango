package main

import (
	"context"
	"fmt"
	"os"

	"github.com/tsumina/dango/internal/cli"
)

func main() {
	app := cli.New(os.Stdout, os.Stderr)
	if err := app.Run(context.Background(), os.Args[1:]); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
