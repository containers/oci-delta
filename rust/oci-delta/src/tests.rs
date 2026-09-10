use super::*;
use std::io::{Cursor, Seek, SeekFrom};

fn uvarint(bytes: &[u8]) -> Result<u64> {
    read_uvarint(&mut io::BufReader::new(bytes))
}

fn write_uvarint(out: &mut Vec<u8>, mut value: u64) {
    while value >= 0x80 {
        out.push(value as u8 | 0x80);
        value >>= 7;
    }
    out.push(value as u8);
}

/// Assemble a tar-diff stream from `(op, size, data)` triples. Ops without
/// a payload (Copy, Seek) carry their operand in `size` and empty `data`.
fn build_tar_diff(header: &[u8; 8], ops: &[(u8, u64, &[u8])]) -> Vec<u8> {
    let mut body = Vec::new();
    for (op, size, data) in ops {
        body.push(*op);
        write_uvarint(&mut body, *size);
        body.extend_from_slice(data);
    }
    let mut out = header.to_vec();
    out.extend_from_slice(&zstd::stream::encode_all(&body[..], 3).unwrap());
    out
}

/// A zstd frame compressing `target` against `source` as a raw dictionary,
/// as `zstd --patch-from` and tar-diff's zstd backend produce.
fn zstd_patch_from(source: &[u8], target: &[u8]) -> Vec<u8> {
    zstd_patch_from_with_window_log(source, target, None)
}

/// As [`zstd_patch_from`], but able to declare a window larger than the
/// libzstd encoder default. tar-diff's Go encoder sizes the window to the
/// source file, so real deltas of sources over 128 MiB only decode with a
/// raised `window_log_max`.
fn zstd_patch_from_with_window_log(
    source: &[u8],
    target: &[u8],
    window_log: Option<u32>,
) -> Vec<u8> {
    let mut encoder = zstd::stream::write::Encoder::with_ref_prefix(Vec::new(), 3, source).unwrap();
    if let Some(window_log) = window_log {
        encoder
            .set_parameter(zstd::stream::raw::CParameter::WindowLog(window_log))
            .unwrap();
    }
    encoder.write_all(target).unwrap();
    encoder.finish().unwrap()
}

const SOURCE_NAME: &str = "data/blob.bin";

struct MemorySource(Cursor<Vec<u8>>);
impl DeltaDataSource for MemorySource {
    fn set_current_file(&mut self, path: &str) -> Result<()> {
        ensure!(path == SOURCE_NAME, "Unknown source file {path}");
        self.0.set_position(0);
        Ok(())
    }
    fn read_exact_current(&mut self, buf: &mut [u8]) -> Result<()> {
        Ok(self.0.read_exact(buf)?)
    }
    fn seek_current(&mut self, offset: u64) -> Result<u64> {
        Ok(self.0.seek(SeekFrom::Start(offset))?)
    }
    fn read_current_to_end(&mut self, max_size: u64) -> Result<Vec<u8>> {
        ensure!(
            self.0.get_ref().len() as u64 <= max_size,
            "Source file too large"
        );
        self.0.set_position(0);
        let mut bytes = Vec::new();
        self.0.read_to_end(&mut bytes)?;
        Ok(bytes)
    }
    fn copy_to(&mut self, dst: &mut dyn Write, n: u64) -> Result<()> {
        let count = io::copy(&mut Read::by_ref(&mut self.0).take(n), dst)?;
        ensure!(count == n, "Short source read");
        Ok(())
    }
}
#[tokio::test]
async fn test_tar_patch_zstd_dict() {
    let (source, target) = similar_blobs();
    let patch = zstd_patch_from(&source, &target);
    assert!(patch.len() < target.len() / 4, "patch should be small");

    let delta = build_tar_diff(
        TAR_DIFF_HEADER_V2,
        &[
            (OP_OPEN, SOURCE_NAME.len() as u64, SOURCE_NAME.as_bytes()),
            (OP_ZSTD_DICT, patch.len() as u64, &patch),
        ],
    );

    let mut data_source = MemorySource(Cursor::new(source.clone()));
    let mut out = Vec::new();
    tar_patch_apply(&delta[..], &mut data_source, &mut out).expect("applying zstd-dict delta");
    assert_eq!(out, target);
}

/// libzstd refuses windows above 128 MiB by default, so a frame declaring
/// the 512 MiB window that tar-diff allows only decodes because
/// [`MAX_ZSTD_DICT_WINDOW_LOG`] raises the cap — and anything beyond it is
/// still refused.
#[tokio::test]
async fn test_tar_patch_zstd_dict_window_log() {
    let (source, target) = similar_blobs();

    for (window_log, accepted) in [
        (MAX_ZSTD_DICT_WINDOW_LOG, true),
        (MAX_ZSTD_DICT_WINDOW_LOG + 1, false),
    ] {
        let patch = zstd_patch_from_with_window_log(&source, &target, Some(window_log));
        let delta = build_tar_diff(
            TAR_DIFF_HEADER_V2,
            &[
                (OP_OPEN, SOURCE_NAME.len() as u64, SOURCE_NAME.as_bytes()),
                (OP_ZSTD_DICT, patch.len() as u64, &patch),
            ],
        );

        let mut data_source = MemorySource(Cursor::new(source.clone()));
        let mut out = Vec::new();
        let result = tar_patch_apply(&delta[..], &mut data_source, &mut out);
        assert_eq!(
            result.is_ok(),
            accepted,
            "windowLog {window_log}: {result:?}"
        );
        if accepted {
            assert_eq!(out, target);
        }
    }
}

#[tokio::test]
async fn test_tar_patch_rejects_unknown_header() {
    let delta = build_tar_diff(b"tardf3\n\0", &[(OP_DATA, 5, b"hello")]);

    let mut data_source = MemorySource(Cursor::new(Vec::new()));
    tar_patch_apply(&delta[..], &mut data_source, &mut Vec::new())
        .expect_err("unknown tar-diff version must be rejected");
}

#[test]
fn test_read_uvarint() {
    assert_eq!(uvarint(&[0]).unwrap(), 0);
    assert_eq!(uvarint(&[1]).unwrap(), 1);
    assert_eq!(uvarint(&[0x7f]).unwrap(), 127);
    assert_eq!(uvarint(&[0x80, 0x01]).unwrap(), 128);
    assert_eq!(uvarint(&[0xac, 0x02]).unwrap(), 300);
    assert_eq!(uvarint(&[0xff, 0x7f]).unwrap(), 16383);
    assert_eq!(uvarint(&[0x80, 0x80, 0x01]).unwrap(), 16384);
    // u64::MAX = 0xffff_ffff_ffff_ffff
    assert_eq!(
        uvarint(&[0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01]).unwrap(),
        u64::MAX,
    );
}

#[test]
fn test_read_uvarint_overflow() {
    // 10 bytes with all continuation bits set overflows shift
    assert!(uvarint(&[0x80; 10]).is_err());
    // 11 continuation bytes
    assert!(uvarint(&[0x80; 11]).is_err());
    // 10th byte value > 1 overflows u64 (2 << 63 > u64::MAX)
    assert!(uvarint(&[0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x02]).is_err());
    // 10th byte value == 1 is the last valid encoding (1 << 63 fits)
    assert_eq!(
        uvarint(&[0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01]).unwrap(),
        u64::MAX,
    );
}

#[test]
fn test_read_uvarint_truncated() {
    // Continuation bit set but no more bytes
    assert!(uvarint(&[0x80]).is_err());
    assert!(uvarint(&[]).is_err());
}

fn similar_blobs() -> (Vec<u8>, Vec<u8>) {
    let source: Vec<u8> = (0..32768u32)
        .map(|i| (i.wrapping_mul(2654435761) >> 13) as u8)
        .collect();
    let mut target = source.clone();
    target[10000..10256].fill(0x5a);
    target.extend_from_slice(b"appended target-only bytes");
    (source, target)
}

use serde_json::{Value, json};

#[test]
fn v1_copy_seek_and_add_data_preserve_source_position() {
    let delta = build_tar_diff(
        TAR_DIFF_HEADER_V1,
        &[
            (OP_OPEN, SOURCE_NAME.len() as u64, SOURCE_NAME.as_bytes()),
            (OP_COPY, 2, b""),
            (OP_SEEK, 4, b""),
            (OP_ADD_DATA, 2, &[10, 255]),
            (OP_COPY, 1, b""),
            (OP_SEEK, 1, b""),
            (OP_COPY, 2, b""),
        ],
    );
    let mut source = MemorySource(Cursor::new(vec![10, 20, 30, 40, 250, 2, 70]));
    let mut out = Vec::new();
    tar_patch_apply(&delta[..], &mut source, &mut out).unwrap();
    assert_eq!(out, [10, 20, 4, 1, 70, 20, 30]);
}

#[test]
fn v1_rejects_short_operation_payloads_and_source_reads() {
    for (label, ops) in [
        ("data payload", vec![(OP_DATA, 3, &b"ab"[..])]),
        ("filename payload", vec![(OP_OPEN, 3, &b"ab"[..])]),
        (
            "add-data payload",
            vec![
                (OP_OPEN, SOURCE_NAME.len() as u64, SOURCE_NAME.as_bytes()),
                (OP_ADD_DATA, 3, &b"ab"[..]),
            ],
        ),
        (
            "copy source",
            vec![
                (OP_OPEN, SOURCE_NAME.len() as u64, SOURCE_NAME.as_bytes()),
                (OP_COPY, 5, &b""[..]),
            ],
        ),
        (
            "add-data source",
            vec![
                (OP_OPEN, SOURCE_NAME.len() as u64, SOURCE_NAME.as_bytes()),
                (OP_ADD_DATA, 5, &b"abcde"[..]),
            ],
        ),
    ] {
        let delta = build_tar_diff(TAR_DIFF_HEADER_V1, &ops);
        let mut source = MemorySource(Cursor::new(b"abcd".to_vec()));
        assert!(
            tar_patch_apply(&delta[..], &mut source, &mut Vec::new()).is_err(),
            "accepted short {label}"
        );
    }
}

fn digest(bytes: &[u8], algorithm: &str) -> OciDigest {
    use openssl::hash::{MessageDigest, hash};
    let md = match algorithm {
        "sha256" => MessageDigest::sha256(),
        "sha384" => MessageDigest::sha384(),
        "sha512" => MessageDigest::sha512(),
        _ => panic!("unexpected test algorithm"),
    };
    format!("{algorithm}:{}", hex::encode(hash(md, bytes).unwrap()))
        .parse()
        .unwrap()
}

#[test]
fn reconstructs_layer_encodings_and_verifies_all_diff_id_algorithms() {
    let target = b"uncompressed layer bytes\0with binary content\xff";
    let mut gzip = flate2::write::GzEncoder::new(Vec::new(), flate2::Compression::default());
    gzip.write_all(target).unwrap();
    let encodings = [
        (MediaType::ImageLayer, target.to_vec()),
        (MediaType::ImageLayerGzip, gzip.finish().unwrap()),
        (
            MediaType::ImageLayerZstd,
            zstd::stream::encode_all(&target[..], 3).unwrap(),
        ),
        (
            media_type_tar_diff(),
            build_tar_diff(
                TAR_DIFF_HEADER_V1,
                &[(OP_DATA, target.len() as u64, target)],
            ),
        ),
    ];
    for (media_type, blob) in encodings {
        for algorithm in ["sha256", "sha384", "sha512"] {
            let mut source = MemorySource(Cursor::new(Vec::new()));
            let mut out = Vec::new();
            reconstruct_layer_to(
                Cursor::new(blob.clone()),
                &media_type,
                &mut source,
                &digest(target, algorithm),
                &mut out,
            )
            .unwrap();
            assert_eq!(out, target, "{media_type}, {algorithm}");

            let error = reconstruct_layer_to(
                Cursor::new(blob.clone()),
                &media_type,
                &mut source,
                &digest(b"different uncompressed bytes", algorithm),
                &mut Vec::new(),
            )
            .unwrap_err();
            assert!(
                error.to_string().contains("diff_id mismatch"),
                "{media_type}, {algorithm}: {error:#}"
            );
        }
    }
}

struct MemoryBlobs(HashMap<OciDigest, Vec<u8>>);

impl DeltaBlobReader for MemoryBlobs {
    fn open_blob(&self, desc: &Descriptor) -> BlobStreamFuture<'_> {
        let bytes = self.0.get(desc.digest()).cloned();
        Box::pin(async move {
            let bytes = bytes.context("Missing in-memory blob")?;
            Ok(Box::new(Cursor::new(bytes)) as Box<dyn BlobStream>)
        })
    }
}

fn descriptor(media_type: &str, bytes: &[u8]) -> Value {
    json!({
        "mediaType": media_type,
        "digest": digest(bytes, "sha256").to_string(),
        "size": bytes.len()
    })
}

fn manifest_fixture() -> (Value, MemoryBlobs, Vec<u8>, Vec<u8>, Value) {
    let config_raw = br#"{
  "rootfs": {"diff_ids": [], "type": "layers"},
  "os": "linux", "architecture": "amd64"
}
"#
    .to_vec();
    let mut config_desc = descriptor("application/vnd.oci.image.config.v1+json", &config_raw);
    let target_layer = descriptor(
        "application/vnd.oci.image.layer.v1.tar+gzip",
        b"target layer",
    );
    let target = json!({
        "schemaVersion": 2,
        "mediaType": "application/vnd.oci.image.manifest.v1+json",
        "config": config_desc,
        "layers": [target_layer]
    });
    let mut manifest_raw = serde_json::to_vec_pretty(&target).unwrap();
    manifest_raw.extend_from_slice(b"\n \t");
    let mut manifest_desc = descriptor("application/vnd.oci.image.manifest.v1+json", &manifest_raw);
    manifest_desc["annotations"] = json!({ANNOTATION_DELTA_CONTENT: "image-manifest"});
    config_desc["annotations"] = json!({ANNOTATION_DELTA_CONTENT: "image-config"});
    let mut patch_desc = descriptor("application/vnd.tar-diff", b"patch blob");
    patch_desc["annotations"] = json!({
        ANNOTATION_DELTA_CONTENT: "image-layer",
        ANNOTATION_DELTA_TO: target_layer["digest"]
    });
    let delta = json!({
        "schemaVersion": 2,
        "artifactType": MEDIA_TYPE_DELTA,
        "config": descriptor("application/vnd.oci.empty.v1+json", b"{}"),
        "layers": [patch_desc, config_desc, manifest_desc],
        "annotations": {
            ANNOTATION_DELTA_SOURCE_CONFIG: digest(b"source config", "sha256").to_string()
        }
    });
    let reader = MemoryBlobs(HashMap::from([
        (digest(&manifest_raw, "sha256"), manifest_raw.clone()),
        (digest(&config_raw, "sha256"), config_raw.clone()),
    ]));
    (delta, reader, manifest_raw, config_raw, target)
}

#[tokio::test]
async fn parses_embedded_manifest_preserving_bytes_and_target_layer_mapping() {
    let (delta, reader, manifest_raw, config_raw, target) = manifest_fixture();
    let manifest: ImageManifest = serde_json::from_value(delta.clone()).unwrap();
    let parsed = parse_delta_manifest(&manifest, &reader).await.unwrap();
    assert_eq!(parsed.target_manifest_raw, manifest_raw);
    assert_eq!(parsed.target_config_raw, config_raw);
    assert_eq!(
        parsed.target_manifest,
        serde_json::from_value::<ImageManifest>(target.clone()).unwrap()
    );
    assert_eq!(
        parsed.target_manifest_descriptor,
        serde_json::from_value::<Descriptor>(delta["layers"][2].clone()).unwrap()
    );
    assert_eq!(
        parsed.target_config_descriptor,
        serde_json::from_value::<Descriptor>(delta["layers"][1].clone()).unwrap()
    );
    assert_eq!(
        parsed.source_config_digest,
        digest(b"source config", "sha256")
    );
    let to: OciDigest = target["layers"][0]["digest"]
        .as_str()
        .unwrap()
        .parse()
        .unwrap();
    assert_eq!(
        parsed.delta_layer_by_to,
        HashMap::from([(
            to,
            serde_json::from_value::<Descriptor>(delta["layers"][0].clone()).unwrap()
        )])
    );
}

#[tokio::test]
async fn rejects_missing_required_delta_metadata() {
    for (missing, expected_error) in [
        ("annotations", "no annotations"),
        ("source-config", "missing source config digest"),
        ("image-manifest", "no embedded image manifest"),
        ("image-config", "no embedded image config"),
    ] {
        let (mut delta, reader, _, _, _) = manifest_fixture();
        match missing {
            "annotations" => {
                delta.as_object_mut().unwrap().remove("annotations");
            }
            "source-config" => {
                delta["annotations"]
                    .as_object_mut()
                    .unwrap()
                    .remove(ANNOTATION_DELTA_SOURCE_CONFIG);
            }
            content => delta["layers"].as_array_mut().unwrap().retain(|layer| {
                layer["annotations"][ANNOTATION_DELTA_CONTENT].as_str() != Some(content)
            }),
        }
        let manifest: ImageManifest = serde_json::from_value(delta).unwrap();
        let error = parse_delta_manifest(&manifest, &reader).await.unwrap_err();
        assert!(
            error.to_string().contains(expected_error),
            "{missing}: {error:#}"
        );
    }
}
