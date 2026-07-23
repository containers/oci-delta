package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	CompletionOptions: cobra.CompletionOptions{
		DisableDefaultCmd: true,
	},
	Use:   "oci-delta",
	Short: "Create and apply OCI image deltas",
	Long: `oci-delta is a tool for creating and applying deltas between OCI images.
It supports creating efficient delta images, applying deltas to reconstruct full images,
and importing delta images directly into container storage.`,
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

var logLevel slog.LevelVar

type messageOnlyHandler struct{}

func (h *messageOnlyHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= logLevel.Level()
}

func (h *messageOnlyHandler) Handle(_ context.Context, r slog.Record) error {
	_, err := fmt.Fprintln(os.Stderr, r.Message)
	return err
}

func (h *messageOnlyHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *messageOnlyHandler) WithGroup(_ string) slog.Handler      { return h }

func init() {
	slog.SetDefault(slog.New(&messageOnlyHandler{}))
}

// Root returns the root cobra command for use by documentation generators.
func Root() *cobra.Command {
	return rootCmd
}

// Logger interface for command output
type Logger interface {
	Debug(format string, args ...interface{})
	Warning(format string, args ...interface{})
	Info(msg string)
	Infof(format string, args ...interface{})
}

// cmdLogger implements the Logger interface.
// Info/Infof delegate to slog.Default(); Debug/Warning retain the legacy behavior until the full slog migration (see issue #67).
type cmdLogger struct {
	debug bool
}

func (l *cmdLogger) Info(msg string) {
	slog.Info(msg)
}

func (l *cmdLogger) Infof(format string, args ...interface{}) {
	slog.Info(fmt.Sprintf(format, args...))
}

func (l *cmdLogger) Debug(format string, args ...interface{}) {
	if l.debug {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
	}
}

func (l *cmdLogger) Warning(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "Warning: "+format+"\n", args...)
}
