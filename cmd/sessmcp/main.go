package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/zytyan/sessmcp/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if e := cli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); e != nil {
		fmt.Fprintln(os.Stderr, "sessmcp:", e)
		os.Exit(1)
	}
}
