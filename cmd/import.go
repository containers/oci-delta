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
	importContainerStorage string
	importTag              string
	importVerifyKey        string
)

var importCmd = &cobra.Command{
	Use:   "import [OPTIONS] <delta-file>",
	Short: "Apply a delta and import the result into container storage",
	Long: `Apply a delta and import the result into container storage.

Arguments:
  <delta-file>  Path to the delta file`,
	Args: cobra.ExactArgs(1),
	RunE: runImport,
}

func init() {
	rootCmd.AddCommand(importCmd)

	importCmd.Flags().StringVar(&importContainerStorage, "container-storage", "", "podman container storage root (default: system default)")
	importCmd.Flags().StringVarP(&importTag, "tag", "t", "", "tag name for the imported image")
	importCmd.Flags().StringVar(&importVerifyKey, "verify-key", "", "path to cosign public key PEM file for signature verification")
	addLogFlags(importCmd)
}

func runImport(cmd *cobra.Command, args []string) error {
	store, err := ocidelta.OpenContainerStorage(importContainerStorage)
	if err != nil {
		return err
	}
	defer func() { store.Shutdown(false) }()

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

	if importVerifyKey != "" {
		log.Debug("Verifying signature", "key", importVerifyKey)

		verifier, err := sigstoreSignature.LoadVerifierFromPEMFile(importVerifyKey, crypto.SHA256)
		if err != nil {
			return fmt.Errorf("failed to load verification key %s: %w", importVerifyKey, err)
		}

		if err := ocidelta.VerifyDeltaSignature(delta, verifier, log); err != nil {
			return fmt.Errorf("signature verification failed: %w", err)
		}
	}

	dataSource, err := ocidelta.ResolveContainerStorageDataSource(store, delta.SourceConfigDigest(), log)
	if err != nil {
		return fmt.Errorf("failed to create data source: %w", err)
	}

	imageID, err := ocidelta.ImportDelta(delta, store, dataSource, ocidelta.ImportOptions{
		Tag:    importTag,
		TmpDir: tmpDir,
	}, log)
	if err != nil {
		return err
	}

	fmt.Println(imageID)

	return nil
}
