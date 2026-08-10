package cmd

import (
	"crypto"
	"fmt"
	"os"

	ocidelta "github.com/containers/oci-delta/pkg/oci-delta"
	sigstoreSignature "github.com/sigstore/sigstore/pkg/signature"
	"github.com/spf13/cobra"
)

var (
	applyRepoPath         string
	applyDirectorySource  string
	applyContainerStorage string
	applyVerifyKey        string
	repoExplicit          bool
)

var applyCmd = &cobra.Command{
	Use:   "apply [OPTIONS] <delta-file> <output>",
	Short: "Apply a delta to reconstruct a full OCI image",
	Long: `Apply a delta to reconstruct a full OCI image.

Arguments:
  <delta-file>  Path to the delta file
  <output>      Output image (oci-archive:path or oci:path)

If no type prefix is given, oci-archive: is assumed.`,
	Args: cobra.ExactArgs(2),
	PreRunE: func(cmd *cobra.Command, args []string) error {
		repoExplicit = cmd.Flags().Changed("ostree-repo")
		return nil
	},
	RunE: runApply,
}

func init() {
	rootCmd.AddCommand(applyCmd)

	applyCmd.Flags().StringVar(&applyRepoPath, "ostree-repo", "/ostree/repo", "ostree repository path (auto-detects source ref via config digest)")
	applyCmd.Flags().StringVar(&applyDirectorySource, "directory", "", "source directory for delta reconstruction (alternative to --ostree-repo)")
	applyCmd.Flags().StringVar(&applyContainerStorage, "container-storage", "", "podman container storage root for delta reconstruction (alternative to --ostree-repo)")
	applyCmd.Flags().StringVar(&applyVerifyKey, "verify-key", "", "path to cosign public key PEM file for signature verification")
	addLogFlags(applyCmd)
}

func runApply(cmd *cobra.Command, args []string) error {
	sourceCount := 0
	if repoExplicit {
		sourceCount++
	}

	if applyDirectorySource != "" {
		sourceCount++
	}

	if applyContainerStorage != "" {
		sourceCount++
	}

	if sourceCount > 1 {
		return fmt.Errorf("--ostree-repo, --directory, and --container-storage are mutually exclusive")
	}

	tmpDir, err := os.MkdirTemp("/var/tmp", "oci-delta-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	log := newLogger()

	log.Debug("Opening delta", "delta", args[0])

	deltaReader, err := ocidelta.OpenOCIReader(args[0], tmpDir, log)
	if err != nil {
		return fmt.Errorf("failed to open delta: %w", err)
	}

	log.Debug("Parsing delta...")

	delta, err := ocidelta.ParseDeltaArtifact(deltaReader, log)
	if err != nil {
		deltaReader.Close()
		return err
	}
	defer delta.Close()

	if applyVerifyKey != "" {
		log.Debug("verifying signature", "key", applyVerifyKey)

		verifier, err := sigstoreSignature.LoadVerifierFromPEMFile(applyVerifyKey, crypto.SHA256)
		if err != nil {
			return fmt.Errorf("failed to load verification key %s: %w", applyVerifyKey, err)
		}

		if err := ocidelta.VerifyDeltaSignature(delta, verifier, log); err != nil {
			return fmt.Errorf("signature verification failed: %w", err)
		}
	}

	var dataSource ocidelta.DataSource
	switch {
	case applyDirectorySource != "":
		dataSource = ocidelta.NewFilesystemDataSource(applyDirectorySource)
	case applyContainerStorage != "":
		store, err := ocidelta.OpenContainerStorage(applyContainerStorage)
		if err != nil {
			return err
		}
		defer func() { store.Shutdown(false) }()

		dataSource, err = ocidelta.ResolveContainerStorageDataSource(store, delta.SourceConfigDigest(), log)
		if err != nil {
			return err
		}
	default:
		ds, err := ocidelta.ResolveOstreeDataSource(applyRepoPath, delta.SourceConfigDigest(), log)
		if err != nil {
			return err
		}
		dataSource = ds
	}

	defer func() {
		_ = dataSource.Close()
		_ = dataSource.Cleanup()
	}()

	writer, err := ocidelta.OpenOCIWriter(args[1])
	if err != nil {
		return fmt.Errorf("failed to create output: %w", err)
	}

	if err := ocidelta.ApplyDelta(delta, writer, dataSource, ocidelta.ApplyOptions{
		TmpDir: tmpDir,
	}, log); err != nil {
		writer.Close()
		return err
	}

	return writer.Close()
}
