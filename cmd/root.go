package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var (
	logLevelFlag string
	debugFlag    bool
	quietFlag    bool
)

var logLevel slog.LevelVar

var rootCmd = &cobra.Command{
	CompletionOptions: cobra.CompletionOptions{
		DisableDefaultCmd: true,
	},
	Use:   "oci-delta",
	Short: "Create and apply OCI image deltas",
	Long: `oci-delta is a tool for creating and applying deltas between OCI images.
It supports creating efficient delta images, applying deltas to reconstruct full images,
and importing delta images directly into container storage.`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		switch {
		case debugFlag:
			logLevel.Set(slog.LevelDebug)
		case quietFlag:
			logLevel.Set(slog.LevelError)
		default:
			level, err := parseLogLevel(logLevelFlag)
			if err != nil {
				return err
			}
			logLevel.Set(level)
		}

		return nil
	},
}

func init() {
	slog.SetDefault(slog.New(&messageOnlyHandler{}))
}

// addLogFlags registers the --log-level, --debug, and --quiet flags on cmd. These are
// registered locally on each subcommand (rather than as persistent flags on rootCmd) so
// they only appear in that subcommand's own help output, since they have no effect
// unless combined with a subcommand.
func addLogFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&logLevelFlag, "log-level", "info", "log verbosity level: error, warn, info, or debug")
	cmd.Flags().BoolVar(&debugFlag, "debug", false, "enable debug output (shorthand for --log-level=debug)")
	cmd.Flags().BoolVarP(&quietFlag, "quiet", "q", false, "suppress all output except errors (shorthand for --log-level=error)")
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

// Root returns the root cobra command for use by documentation generators.
func Root() *cobra.Command {
	return rootCmd
}

func newLogger() *slog.Logger {
	return slog.Default()
}

func parseLogLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "error":
		return slog.LevelError, nil
	case "warn":
		return slog.LevelWarn, nil
	case "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	default:
		return 0, fmt.Errorf("invalid log level %q (valid: error, warn, info, debug)", s)
	}
}

type messageOnlyHandler struct{}

func (h *messageOnlyHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= logLevel.Level()
}

func (h *messageOnlyHandler) Handle(_ context.Context, r slog.Record) error {
	var prefix string
	switch {
	case r.Level >= slog.LevelError:
		prefix = "ERROR: "
	case r.Level >= slog.LevelWarn:
		prefix = "WARNING: "
	}
	msg := r.Message
	r.Attrs(func(a slog.Attr) bool {
		msg += " " + a.Key + "=" + a.Value.String()
		return true
	})
	_, err := fmt.Fprintln(os.Stderr, prefix+msg)

	return err
}

func (h *messageOnlyHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *messageOnlyHandler) WithGroup(_ string) slog.Handler      { return h }
