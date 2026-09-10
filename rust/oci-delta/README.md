# oci-delta (Rust)

Storage-independent parsing and application of OCI delta artifacts.
The Go implementation remains the delta producer.

Implement `DeltaBlobReader` to read artifact blobs and `DeltaDataSource` to read
files from the source image. `parse_delta_manifest` returns the embedded target
metadata and changed-layer mapping; it parses metadata but does not authenticate
it or verify its digests. Consumers must validate the raw manifest/config bytes
and establish trust in the target digest themselves.

`reconstruct_layer_to` applies tar-diff v1/v2 patches or decompresses whole layers
and verifies the resulting diff-ID. Output is streamed before verification
completes: publish it only after success. Missing source files are errors; this
crate does not fetch images.

Building requires Rust 1.88 or newer, a C compiler, pkg-config, and the system
OpenSSL development files. Hashing uses system OpenSSL, as in composefs-rs.

Run `cargo test --manifest-path rust/oci-delta/Cargo.toml` from the repository root.
The source was moved from composefs-rs under its MIT OR Apache-2.0 license.
