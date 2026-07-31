package cmd

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	ocidelta "github.com/containers/oci-delta/pkg/oci-delta"
	"github.com/spf13/cobra"
)

var (
	createVerbose     bool
	createDebug       bool
	createQuiet       bool
	createParallelism int
	createSignatures  []string
)

var createCmd = &cobra.Command{
	Use:   "create [OPTIONS] <old-image> <new-image> <output>",
	Short: "Create a delta between two OCI images",
	Long: `Create a delta between two OCI images.

Arguments:
  <old-image>   Old image (oci-archive:path, oci:path, or containers-storage:ref)
  <new-image>   New image (oci-archive:path, oci:path, or containers-storage:ref)
  <output>      Output delta (oci-archive:path or oci:path)

If no type prefix is given, oci-archive: is assumed.`,
	Args: cobra.ExactArgs(3),
	RunE: runCreate,
}

func init() {
	rootCmd.AddCommand(createCmd)

	createCmd.Flags().BoolVarP(&createVerbose, "verbose", "v", false, "show statistics after creation")
	createCmd.Flags().BoolVar(&createDebug, "debug", false, "show detailed progress information")
	createCmd.Flags().IntVarP(&createParallelism, "jobs", "j", 0, "max parallel tar-diff workers (default: number of CPUs)")
	createCmd.Flags().StringArrayVar(&createSignatures, "signature", nil, "signature OCI artifact to embed (can be specified multiple times)")
	createCmd.Flags().BoolVarP(&createQuiet, "quiet", "q", false, "suppress all default output (does not affect --debug or --verbose)")
}

// Used in info output for simpler feedback.
func bytesToMB(b int64) string {
	return fmt.Sprintf("%.2f MB", float64(b)/(1000*1000))
}

func runCreate(cmd *cobra.Command, args []string) error {
	tmpDir, err := os.MkdirTemp("/var/tmp", "oci-delta-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// This set of conditions is required here while the custom logger is being phased out.
	if createQuiet && !createDebug {
		logLevel.Set(slog.LevelError)
	}

	log := &cmdLogger{debug: createDebug}

	log.Debug("Opening old image: %s", args[0])

	oldReader, err := ocidelta.OpenOCIReader(args[0], tmpDir, log)
	if err != nil {
		return fmt.Errorf("failed to open old image: %w", err)
	}
	defer oldReader.Close()

	log.Debug("Opening new image: %s", args[1])

	newReader, err := ocidelta.OpenOCIReader(args[1], tmpDir, log)
	if err != nil {
		return fmt.Errorf("failed to open new image: %w", err)
	}
	defer newReader.Close()

	sigReaders := ocidelta.ExtractedSignatures(newReader)

	if len(createSignatures) > 0 {
		log.Infof("Embedding %d signature(s)", len(createSignatures))
	}

	for _, sigPath := range createSignatures {
		log.Debug("Opening signature: %s", sigPath)

		sigReader, err := ocidelta.OpenOCIReader(sigPath, tmpDir, log)
		if err != nil {
			return fmt.Errorf("failed to open signature %s: %w", sigPath, err)
		}
		defer sigReader.Close()
		sigReaders = append(sigReaders, sigReader)
	}

	writer, err := ocidelta.OpenOCIWriter(args[2])
	if err != nil {
		return fmt.Errorf("failed to create output: %w", err)
	}

	stats, err := ocidelta.CreateDelta(oldReader, newReader, writer, ocidelta.CreateOptions{
		TmpDir:      tmpDir,
		Parallelism: createParallelism,
		Signatures:  sigReaders,
	}, log)
	if err != nil {
		writer.Close()
		return err
	}

	if err := writer.Close(); err != nil {
		return fmt.Errorf("failed to write output: %w", err)
	}

	if !createVerbose && stats != nil {
		if stats.ProcessedLayerBytes > 0 {
			saved := stats.ProcessedLayerBytes - stats.TarDiffLayerBytes - stats.OriginalLayerBytes
			log.Infof("\nDelta complete: %d layer(s) diffed, %d reused, bytes saved %.2f%% (%s → %s)", stats.ProcessedLayers, stats.SkippedLayers,
				float64(saved)/float64(stats.ProcessedLayerBytes)*100, bytesToMB(stats.ProcessedLayerBytes), bytesToMB(stats.ProcessedLayerBytes-saved))
		} else {
			log.Infof("\nDelta complete: %d layer(s) diffed, %d reused", stats.ProcessedLayers, stats.SkippedLayers)
		}
	}

	if createVerbose && stats != nil {
		fmt.Printf("\nDelta creation statistics:\n")
		fmt.Printf("  Old image layers: %d\n", stats.OldLayers)
		fmt.Printf("  New image layers: %d\n", stats.NewLayers)
		fmt.Printf("  Processed layers: %d\n", stats.ProcessedLayers)
		fmt.Printf("  Skipped layers:   %d\n", stats.SkippedLayers)
		fmt.Printf("  Processed layer bytes:  %d\n", stats.ProcessedLayerBytes)
		fmt.Printf("  Tar-diff layer bytes:   %d\n", stats.TarDiffLayerBytes)
		fmt.Printf("  Original layer bytes:   %d\n", stats.OriginalLayerBytes)

		if stats.ProcessedLayerBytes > 0 {
			saved := stats.ProcessedLayerBytes - stats.TarDiffLayerBytes - stats.OriginalLayerBytes
			pct := float64(saved) / float64(stats.ProcessedLayerBytes) * 100
			fmt.Printf("  Bytes saved:            %d (%.1f%%)\n\n", saved, pct)
		}
	}

	log.Infof("Delta written to %s", args[2])

	outputPath := strings.TrimPrefix(strings.TrimPrefix(args[2], "oci-archive:"), "oci:")
	if p, _, ok := strings.Cut(outputPath, ":"); ok {
		outputPath = p
	}

	if abs, err := filepath.Abs(outputPath); err == nil {
		outputPath = abs
	}
	log.Debug("Delta filepath output: %s", outputPath)

	return nil
}
