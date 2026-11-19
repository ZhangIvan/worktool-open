// worktool-server has no implicit serve mode, migrations or credential bootstrap.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/ZhangIvan/worktool-open/backend-go/internal/startup"
)

func execute(ctx context.Context, args []string, out, diagnostic io.Writer, serve func(context.Context, startup.Config) error) int {
	f := flag.NewFlagSet("worktool-server", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	path := f.String("config", "", "absolute path to private synthetic configuration")
	check := f.Bool("check-config", false, "validate configuration only; no database or listener")
	run := f.Bool("serve", false, "explicitly start the loopback-only synthetic candidate")
	if err := f.Parse(args); err != nil {
		fmt.Fprintln(diagnostic, "INVALID_COMMAND_ARGUMENTS")
		return 2
	}
	if f.NArg() != 0 || *path == "" || *check == *run {
		fmt.Fprintln(diagnostic, "Specify --config and exactly one of --check-config or --serve.")
		return 2
	}
	c, err := startup.LoadConfig(*path)
	if err != nil {
		fmt.Fprintln(diagnostic, "STARTUP_CONFIG_REJECTED")
		return 1
	}
	if *check {
		fmt.Fprintln(out, "Configuration shape accepted. Database, credentials, gate state and serving remain NOT_RUN.")
		return 0
	}
	if err = serve(ctx, c); err != nil {
		fmt.Fprintln(diagnostic, "SYNTHETIC_SERVER_FAILED; no clean shutdown or readiness claim.")
		return 1
	}
	fmt.Fprintln(out, "Synthetic server stopped after joined cleanup.")
	return 0
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := execute(ctx, os.Args[1:], os.Stdout, os.Stderr, startup.Run)
	stop()
	os.Exit(code)
}
