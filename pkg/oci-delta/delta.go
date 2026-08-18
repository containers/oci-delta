package ocidelta

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"

	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	mediaTypeDelta              = "application/vnd.io.github.containers.oci-delta.v1"
	mediaTypeTarDiff            = "application/vnd.tar-diff"
	annotationDeltaTarget       = "io.github.containers.delta.target"
	annotationDeltaSource       = "io.github.containers.delta.source"
	annotationDeltaSourceConfig = "io.github.containers.delta.source-config"
	annotationDeltaTo           = "io.github.containers.delta.to"
	annotationDeltaReused       = "io.github.containers.delta.reused"
	annotationDeltaReusedDiffID = "io.github.containers.delta.reused-diff-id"
	annotationDeltaContent      = "io.github.containers.delta.content"
)

type EmbeddedSignature struct {
	Manifest v1.Manifest
}

type DeltaArtifact struct {
	reader              OCIReader
	imageManifest       v1.Manifest
	imageConfig         v1.Image
	imageManifestDigest digest.Digest
	imageConfigDigest   digest.Digest
	sourceConfigDigest  string
	deltaLayerByTo      map[digest.Digest]v1.Descriptor
	signatures          []EmbeddedSignature
}

func ParseDeltaArtifact(reader OCIReader, log *slog.Logger) (*DeltaArtifact, error) {
	deltaManifestDigest, err := reader.GetManifestDigest()
	if err != nil {
		return nil, fmt.Errorf("failed to read delta manifest digest: %w", err)
	}
	log.Debug("  Delta manifest", "digest", deltaManifestDigest.Encoded()[:16])

	deltaManifestData, err := readBlob(reader, deltaManifestDigest)
	if err != nil {
		return nil, fmt.Errorf("failed to read delta manifest blob: %w", err)
	}

	var deltaManifest v1.Manifest
	if err := json.Unmarshal(deltaManifestData, &deltaManifest); err != nil {
		return nil, fmt.Errorf("failed to parse delta manifest: %w", err)
	}

	if deltaManifest.ArtifactType != mediaTypeDelta {
		return nil, fmt.Errorf("not a delta artifact (artifactType: %s)", deltaManifest.ArtifactType)
	}

	sourceConfigDigest := deltaManifest.Annotations[annotationDeltaSourceConfig]

	if sourceConfigDigest == "" {
		return nil, fmt.Errorf("delta manifest missing required source config annotation: %s", annotationDeltaSourceConfig)
	}

	var imageManifestDesc, imageConfigDesc *v1.Descriptor
	var sigManifestDescs []v1.Descriptor
	deltaLayerByTo := make(map[digest.Digest]v1.Descriptor)

	for i := range deltaManifest.Layers {
		layer := &deltaManifest.Layers[i]
		switch layer.Annotations[annotationDeltaContent] {
		case "image-manifest":
			imageManifestDesc = layer
		case "image-config":
			imageConfigDesc = layer
		case "image-layer":
			toStr := layer.Annotations[annotationDeltaTo]
			if toStr == "" {
				continue
			}

			toDigest, err := digest.Parse(toStr)
			if err != nil {
				log.Warn("invalid delta.to annotation", "value", toStr, "error", err)
				continue
			}
			deltaLayerByTo[toDigest] = *layer
		case "cosign-signature":
			sigManifestDescs = append(sigManifestDescs, *layer)
		}
	}

	if imageManifestDesc == nil {
		return nil, fmt.Errorf("delta manifest contains no embedded image manifest layer")
	}

	if imageConfigDesc == nil {
		return nil, fmt.Errorf("delta manifest contains no embedded image config layer")
	}

	imageManifestData, err := readBlob(reader, imageManifestDesc.Digest)
	if err != nil {
		return nil, fmt.Errorf("failed to read embedded image manifest blob: %w", err)
	}

	var imageManifest v1.Manifest
	if err := json.Unmarshal(imageManifestData, &imageManifest); err != nil {
		return nil, fmt.Errorf("failed to parse embedded image manifest: %w", err)
	}
	log.Debug("  Image manifest", "digest", imageManifestDesc.Digest.Encoded()[:16], "layers", len(imageManifest.Layers))

	imageConfigData, err := readBlob(reader, imageConfigDesc.Digest)
	if err != nil {
		return nil, fmt.Errorf("failed to read embedded image config blob: %w", err)
	}

	var imageConfig v1.Image
	if err := json.Unmarshal(imageConfigData, &imageConfig); err != nil {
		return nil, fmt.Errorf("failed to parse embedded image config: %w", err)
	}
	log.Debug("  Image config", "digest", imageConfigDesc.Digest.Encoded()[:16], "diff_ids", len(imageConfig.RootFS.DiffIDs))

	var signatures []EmbeddedSignature

	for _, desc := range sigManifestDescs {
		sigData, err := readBlob(reader, desc.Digest)
		if err != nil {
			log.Warn("failed to read signature manifest", "digest", desc.Digest.Encoded()[:16], "error", err)
			continue
		}

		var sigManifest v1.Manifest
		if err := json.Unmarshal(sigData, &sigManifest); err != nil {
			log.Warn("failed to parse signature manifest", "digest", desc.Digest.Encoded()[:16], "error", err)
			continue
		}
		signatures = append(signatures, EmbeddedSignature{Manifest: sigManifest})
		log.Debug("  Signature manifest", "digest", desc.Digest.Encoded()[:16], "layers", len(sigManifest.Layers))
	}

	return &DeltaArtifact{
		reader:              reader,
		imageManifest:       imageManifest,
		imageConfig:         imageConfig,
		imageManifestDigest: imageManifestDesc.Digest,
		imageConfigDigest:   imageConfigDesc.Digest,
		sourceConfigDigest:  sourceConfigDigest,
		deltaLayerByTo:      deltaLayerByTo,
		signatures:          signatures,
	}, nil
}

func (d *DeltaArtifact) Close() error {
	return d.reader.Close()
}

func (d *DeltaArtifact) SourceConfigDigest() string {
	return d.sourceConfigDigest
}

func (d *DeltaArtifact) Signatures() []EmbeddedSignature {
	return d.signatures
}

func (d *DeltaArtifact) ImageManifestDigest() digest.Digest {
	return d.imageManifestDigest
}

func (d *DeltaArtifact) ReadBlob(dgst digest.Digest) ([]byte, error) {
	return readBlob(d.reader, dgst)
}

func (d *DeltaArtifact) GetBlobReader(dgst digest.Digest) (io.ReadSeekCloser, error) {
	r, _, _, err := d.reader.ReadBlob(dgst)
	return r, err
}

func (d *DeltaArtifact) GetBlobSize(dgst digest.Digest) (int64, error) {
	r, size, _, err := d.reader.ReadBlob(dgst)
	if err != nil {
		return 0, err
	}
	r.Close()

	return size, nil
}
