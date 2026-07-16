# OCI-Delta Metadata Analysis

## Executive Summary

This document demonstrates how **oci-delta** stores version information (source and target images) in the delta artifact metadata. When creating a delta between two OCI images (Version A → Version B), all version tracking information is stored in the **delta manifest annotations** as standard OCI metadata.

---

## Test Setup

### Images Used
- **Source Image (Old/Version A)**: `alpine:3.19`
- **Target Image (New/Version B)**: `alpine:3.20`
- **Delta Artifact**: Created using `oci-delta create`

### Commands Executed

```bash
# 1. Pull images as OCI layout format using skopeo
skopeo copy docker://alpine:3.19 oci:alpine-3.19:latest
skopeo copy docker://alpine:3.20 oci:alpine-3.20:latest

# 2. Create delta between the two images
oci-delta create --debug --verbose oci:alpine-3.19 oci:alpine-3.20 oci:alpine-delta
```

---

## Delta Creation Results

### Statistics
```
Old image layers: 1
New image layers: 1
Processed layers: 1
Skipped layers:   0
Processed layer bytes:  3,630,321 bytes (3.5 MB)
Tar-diff layer bytes:   1,153,706 bytes (1.1 MB)
Original layer bytes:   0
Bytes saved:            2,476,615 bytes (68.2%)
```

**Space Savings**: 68.2% reduction in size!

---

## OCI Structure Overview

### Alpine 3.19 (Source Image)
**index.json**:
```json
{
  "schemaVersion": 2,
  "mediaType": "application/vnd.oci.image.index.v1+json",
  "manifests": [
    {
      "mediaType": "application/vnd.oci.image.manifest.v1+json",
      "digest": "sha256:b58899f069c47216f6002a6850143dc6fae0d35eb8b0df9300bbe6327b9c2171",
      "size": 1022,
      "annotations": {
        "org.opencontainers.image.ref.name": "latest"
      }
    }
  ]
}
```

**Key Identifier**: `sha256:b58899f069c47216f6002a6850143dc6fae0d35eb8b0df9300bbe6327b9c2171`

---

### Alpine 3.20 (Target Image)
**index.json**:
```json
{
  "schemaVersion": 2,
  "mediaType": "application/vnd.oci.image.index.v1+json",
  "manifests": [
    {
      "mediaType": "application/vnd.oci.image.manifest.v1+json",
      "digest": "sha256:c64c687cbea9300178b30c95835354e34c4e4febc4badfe27102879de0483b5e",
      "size": 1023,
      "annotations": {
        "org.opencontainers.image.ref.name": "latest"
      }
    }
  ]
}
```

**Key Identifier**: `sha256:c64c687cbea9300178b30c95835354e34c4e4febc4badfe27102879de0483b5e`

---

## Delta Artifact Structure

### Delta index.json
```json
{
  "schemaVersion": 2,
  "mediaType": "application/vnd.oci.image.index.v1+json",
  "manifests": [
    {
      "mediaType": "application/vnd.oci.image.manifest.v1+json",
      "digest": "sha256:05c7ece22ea67adc6babc289454204769d81245ea43e206cae83c0d162fce0c6",
      "size": 1520
    }
  ]
}
```

### Delta Manifest (Full Details)

The delta manifest uses:
- **artifactType**: `application/vnd.io.github.containers.oci-delta.v1`
- **Empty config**: Standard OCI artifact pattern (config is just `{}`)

```json
{
  "schemaVersion": 2,
  "artifactType": "application/vnd.io.github.containers.oci-delta.v1",
  "config": {
    "mediaType": "application/vnd.oci.empty.v1+json",
    "digest": "sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a",
    "size": 2
  },
  "layers": [
    {
      "mediaType": "application/vnd.oci.image.manifest.v1+json",
      "digest": "sha256:c64c687cbea9300178b30c95835354e34c4e4febc4badfe27102879de0483b5e",
      "size": 1023,
      "annotations": {
        "io.github.containers.delta.content": "image-manifest"
      }
    },
    {
      "mediaType": "application/vnd.oci.image.config.v1+json",
      "digest": "sha256:bf8527eb54c3680e728d5b4b383a8ba730d72dae7236fbc8dff97ed6b224a731",
      "size": 612,
      "annotations": {
        "io.github.containers.delta.content": "image-config"
      }
    },
    {
      "mediaType": "application/vnd.tar-diff",
      "digest": "sha256:fd0b67e22142ac7c3f0160500d1d186e654bd15e4004d1e92ca7c842b88b088b",
      "size": 1153706,
      "annotations": {
        "io.github.containers.delta.content": "image-layer",
        "io.github.containers.delta.to": "sha256:25f1d6b1951ac8eb3740558fe94cb83d377bdadf95fd9f98b50d2e1b96130471"
      }
    }
  ],
  "subject": {
    "mediaType": "application/vnd.oci.image.manifest.v1+json",
    "digest": "sha256:c64c687cbea9300178b30c95835354e34c4e4febc4badfe27102879de0483b5e",
    "size": 1023
  },
  "annotations": {
    "io.github.containers.delta.source": "sha256:b58899f069c47216f6002a6850143dc6fae0d35eb8b0df9300bbe6327b9c2171",
    "io.github.containers.delta.source-config": "sha256:83b2b6703a620bf2e001ab57f7adc414d891787b3c59859b1b62909e48dd2242",
    "io.github.containers.delta.target": "sha256:c64c687cbea9300178b30c95835354e34c4e4febc4badfe27102879de0483b5e"
  }
}
```

---

## Critical Metadata: The Annotations

### Where Version Information Lives

The **delta manifest annotations** contain all the version tracking information:

```json
{
  "io.github.containers.delta.source": "sha256:b58899f069c47216f6002a6850143dc6fae0d35eb8b0df9300bbe6327b9c2171",
  "io.github.containers.delta.source-config": "sha256:83b2b6703a620bf2e001ab57f7adc414d891787b3c59859b1b62909e48dd2242",
  "io.github.containers.delta.target": "sha256:c64c687cbea9300178b30c95835354e34c4e4febc4badfe27102879de0483b5e"
}
```

### Annotation Details

| Annotation Key | Value | Meaning |
|----------------|-------|---------|
| `io.github.containers.delta.source` | `sha256:b58899f069c47216f6002a6850143dc6fae0d35eb8b0df9300bbe6327b9c2171` | **Manifest digest** of Alpine 3.19 (OLD/Source Image) |
| `io.github.containers.delta.target` | `sha256:c64c687cbea9300178b30c95835354e34c4e4febc4badfe27102879de0483b5e` | **Manifest digest** of Alpine 3.20 (NEW/Target Image) |
| `io.github.containers.delta.source-config` | `sha256:83b2b6703a620bf2e001ab57f7adc414d891787b3c59859b1b62909e48dd2242` | **Config digest** of Alpine 3.19 (for validation) |

---

## Verification: Matching the Digests

### Alpine 3.19 Manifest Digest
- **From original index.json**: `sha256:b58899f069c47216f6002a6850143dc6fae0d35eb8b0df9300bbe6327b9c2171`
- **In delta annotations as `delta.source`**: `sha256:b58899f069c47216f6002a6850143dc6fae0d35eb8b0df9300bbe6327b9c2171`
- ✅ **MATCH**

### Alpine 3.20 Manifest Digest
- **From original index.json**: `sha256:c64c687cbea9300178b30c95835354e34c4e4febc4badfe27102879de0483b5e`
- **In delta annotations as `delta.target`**: `sha256:c64c687cbea9300178b30c95835354e34c4e4febc4badfe27102879de0483b5e`
- ✅ **MATCH**

---

## How oci-delta Stores Version Information

### Key Findings

1. **Version tracking is in metadata, not binary blobs**
   - All version information is stored as JSON annotations in the delta manifest
   - No need to parse binary data to determine source/target images

2. **Uses cryptographic digests for identification**
   - Source and target images are identified by their **manifest digests** (SHA256)
   - These digests are globally unique and content-addressable
   - Changing a single byte in an image changes its manifest digest

3. **Standard OCI artifact pattern**
   - Uses `artifactType` to identify it as a delta artifact
   - Uses standard OCI annotations for metadata
   - Compatible with OCI registries and tooling

4. **Embedded image information**
   - Delta contains the complete target image manifest as a layer
   - Delta contains the target image config as a layer
   - This makes the delta self-contained

### Delta Layers Explained

The delta manifest contains these types of layers:

| Layer Type | Media Type | Annotation | Purpose |
|------------|------------|------------|---------|
| **Image Manifest** | `application/vnd.oci.image.manifest.v1+json` | `delta.content: "image-manifest"` | Complete manifest of target image (Alpine 3.20) |
| **Image Config** | `application/vnd.oci.image.config.v1+json` | `delta.content: "image-config"` | Complete config of target image |
| **Layer Delta** | `application/vnd.tar-diff` | `delta.content: "image-layer"` + `delta.to: <digest>` | Binary diff that reconstructs the layer |

---

## Practical Implications

### For Developers
- **Easy to query**: Just parse the delta manifest JSON to determine source and target
- **Version verification**: Can verify delta applicability before attempting to apply
- **Audit trail**: Clear record of what delta was created from what source

### For Operations
- **Registry compatible**: Can store deltas in OCI registries using standard tools
- **Signature support**: Can sign delta artifacts like any OCI artifact
- **Tooling support**: Standard OCI tools (skopeo, crane, etc.) can inspect deltas

### For Security
- **Cryptographic verification**: Manifest digests provide integrity guarantees
- **Content-addressable**: Cannot confuse different image versions
- **Transparent metadata**: All information visible without running code

---

## Querying Delta Metadata

### Using skopeo

```bash
# Inspect delta annotations
skopeo inspect --raw oci:alpine-delta | jq '.annotations'

# Get source image digest
skopeo inspect --raw oci:alpine-delta | jq -r '.annotations["io.github.containers.delta.source"]'

# Get target image digest
skopeo inspect --raw oci:alpine-delta | jq -r '.annotations["io.github.containers.delta.target"]'

# Check artifact type
skopeo inspect --raw oci:alpine-delta | jq -r '.artifactType'
```

### Using jq on local OCI layout

```bash
# Get delta manifest digest
DELTA_MANIFEST=$(jq -r '.manifests[0].digest' alpine-delta/index.json | cut -d: -f2)

# Read annotations from manifest
cat "alpine-delta/blobs/sha256/$DELTA_MANIFEST" | jq '.annotations'

# List layers with their types
cat "alpine-delta/blobs/sha256/$DELTA_MANIFEST" | jq -r '.layers[] | "\(.annotations["io.github.containers.delta.content"]): \(.mediaType)"'
```

### Extracting tar-diff layers

```bash
# Find the tar-diff blob digest
TAR_DIFF=$(jq -r '.layers[] | select(.mediaType == "application/vnd.tar-diff") | .digest' alpine-delta/blobs/sha256/$DELTA_MANIFEST | cut -d: -f2)

# Extract the tar-diff blob
cp "alpine-delta/blobs/sha256/$TAR_DIFF" delta.tar-diff

# View tar-diff contents (if readable)
tar -tzf delta.tar-diff | head -20
```

---

## Code Implementation

The metadata is set in `pkg/oci-delta/create.go` (lines 205-216):

```go
deltaAnnotations := map[string]string{
    annotationDeltaTarget:       new.manifestDigest.String(),
    annotationDeltaSource:       old.manifestDigest.String(),
    annotationDeltaSourceConfig: old.configDigest.String(),
}
```

The annotations are read in `pkg/oci-delta/delta.go` (line 60):

```go
sourceConfigDigest := deltaManifest.Annotations[annotationDeltaSourceConfig]
```

---

## Conclusion

**The answer to "How does oci-delta track which delta was created (Version A → Version B)?"**

✅ **In the delta manifest annotations** - specifically:
- `io.github.containers.delta.source` points to the source image manifest digest
- `io.github.containers.delta.target` points to the target image manifest digest
- `io.github.containers.delta.source-config` points to the source config digest

This metadata is:
- ✅ Human-readable (JSON)
- ✅ Machine-parseable
- ✅ Cryptographically verifiable
- ✅ OCI-standard compliant
- ✅ Registry-compatible

**No binary parsing required** - everything is in the manifest JSON!

---

## Appendix: File Tree Structure

```
alpine-3.19/                    # Source image OCI layout
├── index.json                  # Points to manifest
├── oci-layout                  # OCI layout version
└── blobs/
    └── sha256/
        ├── b58899f069...       # Image manifest
        ├── 83b2b6703a...       # Image config
        └── 17a39c0ba9...       # Layer (compressed)

alpine-3.20/                    # Target image OCI layout
├── index.json                  # Points to manifest
├── oci-layout
└── blobs/
    └── sha256/
        ├── c64c687cbe...       # Image manifest
        ├── bf8527eb54...       # Image config
        └── 25f1d6b195...       # Layer (compressed)

alpine-delta/                   # Delta artifact OCI layout
├── index.json                  # Points to delta manifest
├── oci-layout
└── blobs/
    └── sha256/
        ├── 05c7ece22e...       # Delta manifest (with annotations!)
        ├── 44136fa355...       # Empty config ({})
        ├── c64c687cbe...       # Target image manifest (embedded)
        ├── bf8527eb54...       # Target image config (embedded)
        └── fd0b67e221...       # tar-diff binary delta
```

---

**Generated**: 2026-07-16  
**Tool**: oci-delta (built from source)  
**Test Images**: alpine:3.19 → alpine:3.20

---

## FAQ: Historical Annotations

### Q: What about `io.github.containers.delta.from`?

**A: This annotation is NO LONGER USED.**

#### Historical Context

The `io.github.containers.delta.from` annotation was part of an earlier version of the oci-delta format, but was **removed on April 1, 2026** in commit `e028122` by Alexander Larsson.

#### What It Used To Do

In the original format, `delta.from` was a **per-layer annotation** that specified which specific layer(s) from the source image were used to compute the tar-diff for that particular delta layer.

Example from old format:
```json
{
  "mediaType": "application/vnd.tar-diff",
  "digest": "sha256:abc123...",
  "annotations": {
    "io.github.containers.delta.to": "sha256:target-layer-digest",
    "io.github.containers.delta.from": "sha256:source-layer-digest",
    "io.github.containers.delta.from-diff-id": "sha256:source-layer-diff-id"
  }
}
```

This meant each delta layer had a 1:1 relationship with a specific source layer.

#### Why It Was Removed

The format change enabled **multi-source diffing** - tar-diff can now use **ALL layers from the source image** as potential source material, not just a single matched layer. This provides better compression because:

1. Files can be reused from any layer in the old image
2. The tool automatically picks the best source for each file
3. More efficient use of available local content

#### Current Approach

Now, the delta format uses:
- **Top-level annotations** (`delta.source`, `delta.target`) to identify the overall source and target images
- **No per-layer source tracking** - tar-diff analyzes all source layers automatically
- **`delta.to` annotation** on each layer to identify which target layer it reconstructs

From the README.md:
> The `delta.from` layer annotation is no longer used, as we diff against all layers in source image.

#### Migration Impact

- **No action needed** - if you have old deltas with `delta.from`, they won't work with current oci-delta
- **Current format** is documented in this analysis
- **Tools** like `analyze-delta.py` were updated to handle the new format in the same commit

---

**Last Updated**: 2026-07-16  
**Format Version**: Post-e028122 (multi-source diffing)
