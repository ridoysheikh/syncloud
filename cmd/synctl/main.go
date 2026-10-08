// Command synctl is the SynCloud command line.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/ridoysheikh/syncloud/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := cli.NewRoot(os.Stdin, os.Stdout, os.Stderr).ExecuteContext(ctx); err != nil {
		code := cli.ExitCode(err)
		if code == 1 {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
		os.Exit(code)
	}
}
