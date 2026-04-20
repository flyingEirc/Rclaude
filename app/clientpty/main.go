package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"

	"github.com/spf13/cobra"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	cmd, err := newRootCommand(defaultCommandDeps())
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := cmd.ExecuteContext(ctx); err != nil {
		var exitErr *exitStatus
		if errors.As(err, &exitErr) {
			if !exitErr.quiet && exitErr.message != "" {
				_, _ = fmt.Fprintln(os.Stderr, exitErr.message)
			}
			os.Exit(exitErr.code)
		}

		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newRootCommand(deps commandDeps) (*cobra.Command, error) {
	var configPath string

	cmd := &cobra.Command{
		Use:           "rclaude-claude",
		Short:         "Attach the local terminal to a remote claude PTY session",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCommand(cmd.Context(), deps, configPath)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "Path to the daemon YAML config")
	if err := cmd.MarkFlagRequired("config"); err != nil {
		return nil, fmt.Errorf("clientpty: mark config flag required: %w", err)
	}

	return cmd, nil
}
