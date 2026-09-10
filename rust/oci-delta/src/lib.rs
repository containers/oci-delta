//! Parse OCI delta artifacts and reconstruct image layers independently of storage.
//!
//! An oci-delta artifact has `artifactType` set to the delta media type.
//! Its layers contain the target image manifest, config, and changed layer
//! blobs (as tar-diff patches or original gzip layers). Layers identical
//! between source and target (by diff_id) are omitted from the delta.
//! For more information, see <https://github.com/containers/oci-delta>

use std::collections::HashMap;
use std::future::Future;
use std::io::{self, BufRead, BufReader, Read, Write};
use std::pin::Pin;

use anyhow::{Context, Result, bail, ensure};
use oci_spec::image::{
    Descriptor, Digest as OciDigest, DigestAlgorithm, ImageConfiguration, ImageManifest, MediaType,
};

/// A synchronous, movable stream of blob bytes.
pub trait BlobStream: Read + Send {}
impl<T: Read + Send> BlobStream for T {}

/// The `artifactType` value identifying an oci-delta artifact manifest.
pub const MEDIA_TYPE_DELTA: &str = "application/vnd.io.github.containers.oci-delta.v1";

fn media_type_tar_diff() -> MediaType {
    MediaType::Other("application/vnd.tar-diff".to_string())
}
const ANNOTATION_DELTA_SOURCE_CONFIG: &str = "io.github.containers.delta.source-config";
const ANNOTATION_DELTA_TO: &str = "io.github.containers.delta.to";
const ANNOTATION_DELTA_CONTENT: &str = "io.github.containers.delta.content";

const TAR_DIFF_HEADER_V1: &[u8; 8] = b"tardf1\n\0";
const TAR_DIFF_HEADER_V2: &[u8; 8] = b"tardf2\n\0";

// tar-diff opcodes
const OP_DATA: u8 = 0;
const OP_OPEN: u8 = 1;
const OP_COPY: u8 = 2;
const OP_ADD_DATA: u8 = 3;
const OP_SEEK: u8 = 4;
const OP_ZSTD_DICT: u8 = 5;

// DoS protection limits from the Go tar-patch reference implementation
const MAX_FILENAME_SIZE: u64 = 4 * 1024;
const MAX_ADD_DATA_SIZE: u64 = 100 * 1024 * 1024;

/// Bump max dict size to the generator max so that we accept all generated ones
/// but no more.
const MAX_ZSTD_DICT_WINDOW_LOG: u32 = 29;
const MAX_ZSTD_DICT_SIZE: u64 = 1 << MAX_ZSTD_DICT_WINDOW_LOG;

// ─── Blob reader trait ──────────────────────────────────────────────────────

/// The future returned by [`DeltaBlobReader::open_blob`].
pub type BlobStreamFuture<'a> =
    Pin<Box<dyn Future<Output = Result<Box<dyn BlobStream>>> + Send + 'a>>;

/// Trait used by parse_delta_manifest() to read read blobs from a delta artifact by digest.
pub trait DeltaBlobReader: Send + Sync {
    /// Open a blob for reading by digest.
    fn open_blob(&self, desc: &Descriptor) -> BlobStreamFuture<'_>;
}

/// Check whether an OCI manifest is a delta artifact.
pub fn is_delta_artifact(manifest: &ImageManifest) -> bool {
    manifest
        .artifact_type()
        .as_ref()
        .is_some_and(|t| t.to_string() == MEDIA_TYPE_DELTA)
}

// ─── Source image data ──────────────────────────────────────────────────────

/// Supplies file data from the "old" image a tar-diff was created against.
///
/// A tar-diff refers to source files by their path in the old image's root
/// filesystem, reading from one file at a time. Implementations resolve those
/// paths against whatever local storage holds that image.
///
/// This is used by tar_patch_apply() and reconstruct_layer_to().
pub trait DeltaDataSource {
    /// Select `path` in the source image as the current file.
    ///
    /// Subsequent reads and seeks apply to it until the next call. Returns an
    /// error if the path is absent: a delta cannot be applied without its
    /// source data, and there is no meaningful way to continue.
    fn set_current_file(&mut self, path: &str) -> Result<()>;

    /// Fill `buf` from the current file, erroring on a short read.
    fn read_exact_current(&mut self, buf: &mut [u8]) -> Result<()>;

    /// Seek the current file to absolute `offset`.
    fn seek_current(&mut self, offset: u64) -> Result<u64>;

    /// Read the whole current file from offset zero, leaving the cursor at end of file.
    /// Return an error if its size exceeds `max_size`.
    fn read_current_to_end(&mut self, max_size: u64) -> Result<Vec<u8>>;

    /// Copy `n` bytes from the current file to `dst`, erroring on a short read.
    fn copy_to(&mut self, dst: &mut dyn Write, n: u64) -> Result<()>;
}

fn read_uvarint(r: &mut impl io::BufRead) -> Result<u64> {
    let mut result: u64 = 0;
    let mut shift: u8 = 0;
    loop {
        let mut byte = [0u8; 1];
        r.read_exact(&mut byte)?;
        let bits = (byte[0] & 0x7f) as u64;
        ensure!(
            shift < 64 && bits <= (u64::MAX >> shift),
            "uvarint overflow"
        );
        result |= bits << shift;
        if byte[0] & 0x80 == 0 {
            return Ok(result);
        }
        shift = shift.checked_add(7).context("uvarint overflow")?;
    }
}

struct OciHasher {
    algorithm: &'static str,
    inner: openssl::hash::Hasher,
}

impl OciHasher {
    fn new(algorithm: &DigestAlgorithm) -> Result<Self> {
        use openssl::hash::{Hasher, MessageDigest};
        let (algorithm, md) = match algorithm {
            DigestAlgorithm::Sha256 => ("sha256", MessageDigest::sha256()),
            DigestAlgorithm::Sha384 => ("sha384", MessageDigest::sha384()),
            DigestAlgorithm::Sha512 => ("sha512", MessageDigest::sha512()),
            other => bail!("Unsupported digest algorithm: {other}"),
        };
        Ok(Self {
            algorithm,
            inner: Hasher::new(md).context("Creating layer hasher")?,
        })
    }

    fn update(&mut self, data: &[u8]) -> io::Result<()> {
        self.inner.update(data).map_err(io::Error::other)
    }

    fn finalize(mut self) -> Result<OciDigest> {
        let hash = self.inner.finish().context("Finalizing layer digest")?;
        format!("{}:{}", self.algorithm, hex::encode(hash))
            .parse()
            .context("Constructed digest")
    }
}

struct HashingWriter<'a, W: Write + ?Sized> {
    inner: &'a mut W,
    hasher: &'a mut OciHasher,
}

impl<W: Write + ?Sized> Write for HashingWriter<'_, W> {
    fn write(&mut self, buf: &[u8]) -> io::Result<usize> {
        let n = self.inner.write(buf)?;
        self.hasher.update(&buf[..n])?;
        Ok(n)
    }

    fn flush(&mut self) -> io::Result<()> {
        self.inner.flush()
    }
}

/// Apply a tar-diff blob, writing the reconstructed uncompressed tar to `dst`.
///
/// `data_source` supplies file data from the source image the delta was built
/// against. The result is not verified here; callers should check it against
/// the expected diff_id, which [`reconstruct_layer_to`] does.
pub fn tar_patch_apply(
    delta: impl Read,
    data_source: &mut dyn DeltaDataSource,
    mut dst: impl Write,
) -> Result<()> {
    let mut header_buf = [0u8; 8];
    let mut reader = io::BufReader::new(delta);
    reader.read_exact(&mut header_buf)?;
    let is_v2 = if header_buf == *TAR_DIFF_HEADER_V2 {
        true
    } else if header_buf == *TAR_DIFF_HEADER_V1 {
        false
    } else {
        bail!("Invalid tar-diff header");
    };

    let decoder =
        zstd::stream::read::Decoder::new(reader).context("Creating zstd decoder for tar-diff")?;
    let mut r = io::BufReader::new(decoder);

    loop {
        let buf = r.fill_buf()?;
        if buf.is_empty() {
            break;
        }
        let op = buf[0];
        r.consume(1);
        let size = read_uvarint(&mut r)?;

        match op {
            OP_DATA => {
                let copied = io::copy(&mut (&mut r).take(size), &mut dst)?;
                ensure!(
                    copied == size,
                    "Short OP_DATA: expected {size}, got {copied}"
                );
            }
            OP_OPEN => {
                ensure!(
                    size <= MAX_FILENAME_SIZE,
                    "Filename size {size} exceeds limit"
                );
                let mut name_buf = vec![0u8; size as usize];
                r.read_exact(&mut name_buf)?;
                let name =
                    String::from_utf8(name_buf).context("Invalid UTF-8 in tar-diff filename")?;
                data_source.set_current_file(&name)?;
            }
            OP_COPY => {
                data_source.copy_to(&mut dst, size)?;
            }
            OP_ADD_DATA => {
                ensure!(
                    size <= MAX_ADD_DATA_SIZE,
                    "AddData size {size} exceeds limit"
                );
                let mut delta_bytes = vec![0u8; size as usize];
                r.read_exact(&mut delta_bytes)?;
                let mut source_bytes = vec![0u8; size as usize];
                data_source
                    .read_exact_current(&mut source_bytes)
                    .context("Reading source data for AddData")?;
                let n = source_bytes.len();
                for i in 0..n {
                    delta_bytes[i] = delta_bytes[i].wrapping_add(source_bytes[i]);
                }
                dst.write_all(&delta_bytes)?;
            }
            OP_SEEK => {
                data_source.seek_current(size)?;
            }
            OP_ZSTD_DICT => {
                ensure!(is_v2, "ZstdDict op requires a tardf2 delta");
                // The dictionary is the whole source file, read it all
                let dict = data_source
                    .read_current_to_end(MAX_ZSTD_DICT_SIZE)
                    .context("Reading source file as zstd dictionary")?;
                let mut frame = Read::by_ref(&mut r).take(size);
                {
                    let mut decoder =
                        zstd::stream::read::Decoder::with_ref_prefix(&mut frame, &dict)
                            .context("Creating zstd decoder for ZstdDict op")?
                            .single_frame();
                    decoder
                        .window_log_max(MAX_ZSTD_DICT_WINDOW_LOG)
                        .context("Setting zstd window limit for ZstdDict op")?;
                    io::copy(&mut decoder, &mut dst).context("Applying ZstdDict op")?;
                }
                // Skip any unread data from the delta stream to ensure the underlying
                // delta stream is at the end of the op.
                io::copy(&mut frame, &mut io::sink())
                    .context("Skipping trailing bytes after ZstdDict frame")?;
            }
            _ => bail!("Unexpected tar-diff op {op}"),
        }
    }

    Ok(())
}

// ─── Delta layer reconstruction ─────────────────────────────────────────────

/// Wrap a layer blob in the decompressor its `media_type` calls for.
fn decompress_layer(
    reader: impl BlobStream + 'static,
    media_type: &MediaType,
) -> Result<Box<dyn BlobStream>> {
    let buf = BufReader::new(reader);
    match media_type {
        MediaType::ImageLayer | MediaType::ImageLayerNonDistributable => Ok(Box::new(buf)),
        MediaType::ImageLayerGzip | MediaType::ImageLayerNonDistributableGzip => {
            Ok(Box::new(BufReader::new(flate2::read::GzDecoder::new(buf))))
        }
        MediaType::ImageLayerZstd | MediaType::ImageLayerNonDistributableZstd => Ok(Box::new(
            BufReader::new(zstd::stream::read::Decoder::new(buf)?),
        )),
        _ => bail!("Unsupported layer media type: {media_type}"),
    }
}

/// Reconstruct one layer's uncompressed tar into `dst`, verifying its diff_id.
///
/// `blob` is a delta layer blob: either a tar-diff, applied against
/// `data_source`, or the original compressed layer, optionally decompressed,
/// as indicated by `media_type`. Errors if the result does not hash to
/// `expected_diff_id`, so callers must not publish `dst` until this returns
/// successfully.
pub fn reconstruct_layer_to(
    blob: impl BlobStream + 'static,
    media_type: &MediaType,
    data_source: &mut dyn DeltaDataSource,
    expected_diff_id: &OciDigest,
    dst: &mut dyn Write,
) -> Result<()> {
    let mut hasher = OciHasher::new(expected_diff_id.algorithm())?;
    let mut hashing_writer = HashingWriter {
        inner: dst,
        hasher: &mut hasher,
    };

    if is_tar_diff(media_type) {
        tar_patch_apply(blob, data_source, &mut hashing_writer)?;
    } else {
        let mut decoder = decompress_layer(blob, media_type)?;
        io::copy(&mut decoder, &mut hashing_writer)?;
    }

    let computed_diff_id = hasher.finalize()?;
    ensure!(
        computed_diff_id == *expected_diff_id,
        "Layer diff_id mismatch: expected {expected_diff_id}, got {computed_diff_id}",
    );
    Ok(())
}

// ─── Delta manifest parsing ─────────────────────────────────────────────────

/// Whether a delta layer blob is a tar-diff, which must be applied against the
/// source image, rather than an original layer blob to decompress directly.
fn is_tar_diff(media_type: &MediaType) -> bool {
    *media_type == media_type_tar_diff()
}

/// A delta artifact's manifest, with the embedded target image manifest and
/// config resolved.
///
/// Layers of the target image that are absent from `delta_layer_by_to` are
/// unchanged from the source image and are expected to be present locally
/// already, matched by diff_id.
#[derive(Debug)]
pub struct ParsedDelta {
    /// The target image's manifest.
    pub target_manifest: ImageManifest,
    /// Descriptor of the target image's manifest, as carried in the delta.
    pub target_manifest_descriptor: Descriptor,
    /// Raw bytes of the target image's manifest. Preserved verbatim so that
    /// the original digest is reproduced rather than a re-serialized one.
    pub target_manifest_raw: Vec<u8>,
    /// Descriptor of the target image's config, as carried in the delta.
    pub target_config_descriptor: Descriptor,
    /// Raw bytes of the target image's config.
    pub target_config_raw: Vec<u8>,
    /// Digest of the source image's config. The delta is only applicable on a
    /// system that already has the image with this config.
    pub source_config_digest: OciDigest,
    /// Changed layers, keyed by the digest of the target layer each produces.
    /// The value is the descriptor of the blob within the delta artifact.
    pub delta_layer_by_to: HashMap<OciDigest, Descriptor>,
}

/// Parse a delta artifact's manifest and extract the embedded target image
/// manifest, config, and layer mapping. Blobs are fetched via `blob_reader`.
///
/// This parses metadata without authenticating it or verifying blob digests.
/// Callers must validate the raw target bytes and establish trust in their digests.
pub async fn parse_delta_manifest(
    delta_manifest: &ImageManifest,
    blob_reader: &dyn DeltaBlobReader,
) -> Result<ParsedDelta> {
    let annotations = delta_manifest
        .annotations()
        .as_ref()
        .context("Delta manifest has no annotations")?;

    let source_config_digest: OciDigest = annotations
        .get(ANNOTATION_DELTA_SOURCE_CONFIG)
        .context("Delta missing source config digest annotation")?
        .parse()
        .context("Invalid source config digest")?;

    let mut target_manifest_descriptor = None;
    let mut target_config_descriptor = None;
    let mut delta_layer_by_to = HashMap::new();

    for layer in delta_manifest.layers() {
        let layer_annotations = layer.annotations();
        let content = layer_annotations
            .as_ref()
            .and_then(|a| a.get(ANNOTATION_DELTA_CONTENT))
            .map(|s| s.as_str())
            .unwrap_or("");

        match content {
            "image-manifest" => {
                target_manifest_descriptor = Some(layer.clone());
            }
            "image-config" => {
                target_config_descriptor = Some(layer.clone());
            }
            "image-layer" => {
                if let Some(to_str) = layer_annotations
                    .as_ref()
                    .and_then(|a| a.get(ANNOTATION_DELTA_TO))
                    .filter(|s| !s.is_empty())
                {
                    let to_digest: OciDigest = to_str.parse().context("Invalid delta.to digest")?;
                    delta_layer_by_to.insert(to_digest, layer.clone());
                }
            }
            _ => {}
        }
    }

    let target_manifest_descriptor =
        target_manifest_descriptor.context("Delta manifest has no embedded image manifest")?;
    let target_config_descriptor =
        target_config_descriptor.context("Delta manifest has no embedded image config")?;

    let mut target_manifest_raw = Vec::new();
    blob_reader
        .open_blob(&target_manifest_descriptor)
        .await
        .context("Fetching embedded image manifest")?
        .read_to_end(&mut target_manifest_raw)?;
    let target_manifest = ImageManifest::from_reader(&target_manifest_raw[..])
        .context("Parsing embedded image manifest")?;

    let mut target_config_raw = Vec::new();
    blob_reader
        .open_blob(&target_config_descriptor)
        .await
        .context("Fetching embedded image config")?
        .read_to_end(&mut target_config_raw)?;
    // Validate it parses
    ImageConfiguration::from_reader(&target_config_raw[..])
        .context("Parsing embedded image config")?;

    Ok(ParsedDelta {
        target_manifest,
        target_manifest_descriptor,
        target_manifest_raw,
        target_config_descriptor,
        target_config_raw,
        source_config_digest,
        delta_layer_by_to,
    })
}

#[cfg(test)]
mod tests;
