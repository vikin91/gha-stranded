package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gha-stranded/internal/detector"
)

func main() { os.Exit(execute(os.Args[1:], os.Stdout, os.Stderr)) }

func execute(args []string, output, diagnostics io.Writer) int {
	flags := flag.NewFlagSet("gha-stranded", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	minAge := flags.Duration("min-age", 2*time.Hour, "minimum queue age")
	confirm := flags.Duration("confirm-delay", 10*time.Second, "minimum delay before rechecking candidates")
	pr := flags.Int("pr", 0, "analyze one PR number instead of scanning base branches")
	flags.Usage = func() {
		fmt.Fprint(diagnostics, "Usage: gha-stranded [--min-age 2h] [--confirm-delay 10s] [BASE_BRANCH]\n       gha-stranded --pr NUMBER\n\nLists PRs in stackrox/stackrox with missing required Actions checks and\nold, empty queued workflow runs. Only prints cancel/rerun commands.\nDefaults to master and the three highest release-X.Y branches.\nRequires an authenticated gh CLI. Flags must precede the branch.\n")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 1 || *minAge <= 0 || *confirm <= 0 {
		fmt.Fprintln(diagnostics, "Provide at most one base branch and positive durations.")
		return 2
	}
	var prSet bool
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "pr" {
			prSet = true
		}
	})
	if prSet && (*pr <= 0 || flags.NArg() != 0) {
		fmt.Fprintln(diagnostics, "--pr requires a positive PR number and cannot be combined with a base branch.")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := detector.Run(ctx, detector.Options{Branch: flags.Arg(0), PR: *pr, MinAge: *minAge, ConfirmDelay: *confirm}, output, diagnostics)
	if err != nil {
		fmt.Fprintln(diagnostics, "Error:", err)
		if errors.Is(err, context.Canceled) {
			return 130
		}
		return 1
	}
	return 0
}
