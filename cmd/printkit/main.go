package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Vanisper/printkit/internal/render"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	if len(os.Args) == 3 && os.Args[1] == render.WorkerCommand {
		err = render.RunWorker(ctx, os.Args[2], os.Stdin, os.Stdout)
	} else {
		err = run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	}
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "printkit:", err)
		}
		os.Exit(1)
	}
}
