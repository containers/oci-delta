package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	ocidelta "github.com/containers/oci-delta/pkg/oci-delta"
	tardiff "github.com/containers/tar-diff/pkg/tar-diff"
	"github.com/spf13/cobra"
)

var (
	createStatistics        bool
	createVerbose           bool // deprecated alias for --statistics, kept hidden for compatibility
	createParallelism       int
	createSignatures        []string
	createBinaryDiff        string
	createCompressionLevel  int
	createZstdDiffLevel     int
	createZstdDiffWindowMiB int
	createMaxZstdDiffSize   int
	createMaxBsdiffSize     int
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

	createCmd.Flags().BoolVarP(&createStatistics, "statistics", "s", false, "show statistics after creation")
	createCmd.Flags().BoolVarP(&createVerbose, "verbose", "v", false, "show statistics after creation")
	_ = createCmd.Flags().MarkHidden("verbose")
	createCmd.Flags().IntVarP(&createParallelism, "jobs", "j", 0, "max parallel tar-diff workers (default: number of CPUs)")
	createCmd.Flags().StringVar(&createBinaryDiff, "binary-diff", "bsdiff", "per-file binary diff method: bsdiff, zstd, or auto")
	createCmd.Flags().IntVar(&createCompressionLevel, "compression-level", 0, "outer tar-diff zstd level (0 = tar-diff default)")
	createCmd.Flags().IntVar(&createZstdDiffLevel, "zstd-diff-level", -1, "zstd level for dictionary patches (-1 = use compression-level/default)")
	createCmd.Flags().IntVar(&createZstdDiffWindowMiB, "zstd-diff-window", 0, "zstd window size in MiB for dictionary patches (0 = auto, max 512)")
	createCmd.Flags().IntVar(&createMaxZstdDiffSize, "max-zstd-diff-size", 128, "max file size in MiB for zstd dictionary patches (0 = no extra cap)")
	createCmd.Flags().IntVar(&createMaxBsdiffSize, "max-bsdiff-size", 192, "max file size in MiB for bsdiff (0 = no limit)")
	createCmd.Flags().StringArrayVar(&createSignatures, "signature", nil, "signature OCI artifact to embed (can be specified multiple times)")
	addLogFlags(createCmd)
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

	log := newLogger()

	log.Debug("Opening old image", "image", args[0])

	oldReader, err := ocidelta.OpenOCIReader(args[0], tmpDir, log)
	if err != nil {
		return fmt.Errorf("failed to open old image: %w", err)
	}
	defer oldReader.Close()

	log.Debug("Opening new image", "image", args[1])

	newReader, err := ocidelta.OpenOCIReader(args[1], tmpDir, log)
	if err != nil {
		return fmt.Errorf("failed to open new image: %w", err)
	}
	defer newReader.Close()

	sigReaders := ocidelta.ExtractedSignatures(newReader)

	if len(createSignatures) > 0 {
		log.Info(fmt.Sprintf("Embedding %d signature(s)", len(createSignatures)))
	}

	for _, sigPath := range createSignatures {
		log.Debug("opening signature", "path", sigPath)

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

	binaryDiffMethod, err := parseBinaryDiffMethod(createBinaryDiff)
	if err != nil {
		return err
	}
	if createZstdDiffWindowMiB < 0 {
		return fmt.Errorf("invalid --zstd-diff-window %d", createZstdDiffWindowMiB)
	}
	if createMaxZstdDiffSize < 0 {
		return fmt.Errorf("invalid --max-zstd-diff-size %d", createMaxZstdDiffSize)
	}
	if createMaxBsdiffSize < 0 {
		return fmt.Errorf("invalid --max-bsdiff-size %d", createMaxBsdiffSize)
	}

	zstdDiffLevel := createZstdDiffLevel
	zstdDiffWindow := createZstdDiffWindowMiB
	maxZstdDiffSize := createMaxZstdDiffSize
	maxBsdiffSize := createMaxBsdiffSize
	stats, err := ocidelta.CreateDelta(oldReader, newReader, writer, ocidelta.CreateOptions{
		TmpDir:             tmpDir,
		Parallelism:        createParallelism,
		Signatures:         sigReaders,
		BinaryDiffMethod:   binaryDiffMethod,
		CompressionLevel:   createCompressionLevel,
		ZstdDiffLevel:      &zstdDiffLevel,
		ZstdDiffWindowMiB:  &zstdDiffWindow,
		MaxZstdDiffSizeMiB: &maxZstdDiffSize,
		MaxBsdiffSizeMiB:   &maxBsdiffSize,
	}, log)
	if err != nil {
		writer.Close()
		return err
	}

	if err := writer.Close(); err != nil {
		return fmt.Errorf("failed to write output: %w", err)
	}

	showStats := createStatistics || createVerbose

	if !showStats && stats != nil {
		if stats.ProcessedLayerBytes > 0 {
			saved := stats.ProcessedLayerBytes - stats.TarDiffLayerBytes - stats.OriginalLayerBytes
			log.Info(fmt.Sprintf("\nDelta complete: %d layer(s) diffed, %d reused, bytes saved %.2f%% (%s → %s)", stats.ProcessedLayers, stats.SkippedLayers,
				float64(saved)/float64(stats.ProcessedLayerBytes)*100, bytesToMB(stats.ProcessedLayerBytes), bytesToMB(stats.ProcessedLayerBytes-saved)))
		} else {
			log.Info(fmt.Sprintf("\nDelta complete: %d layer(s) diffed, %d reused", stats.ProcessedLayers, stats.SkippedLayers))
		}
	}

	if showStats && stats != nil {
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

	log.Info(fmt.Sprintf("Delta written to %s", args[2]))

	outputPath := strings.TrimPrefix(strings.TrimPrefix(args[2], "oci-archive:"), "oci:")
	if p, _, ok := strings.Cut(outputPath, ":"); ok {
		outputPath = p
	}

	if abs, err := filepath.Abs(outputPath); err == nil {
		outputPath = abs
	}
	log.Debug("Delta filepath output", "path", outputPath)

	return nil
}

func parseBinaryDiffMethod(value string) (tardiff.BinaryDiffMethod, error) {
	switch value {
	case "bsdiff":
		return tardiff.BinaryDiffBsdiff, nil
	case "zstd":
		return tardiff.BinaryDiffZstd, nil
	case "auto":
		return tardiff.BinaryDiffAuto, nil
	default:
		return 0, fmt.Errorf("invalid --binary-diff %q (want bsdiff, zstd, or auto)", value)
	}
}
