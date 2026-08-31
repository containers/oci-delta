% OCI-DELTA(1) oci-delta 0.2.0 | User Commands
% the oci-delta authors
% June 2026

# NAME

oci-delta - create and apply delta files between OCI image archives

# SYNOPSIS

**oci-delta** *subcommand* [*OPTIONS*] [*ARGUMENTS*]

**oci-delta create** [**-s**|**--statistics**] [**--log-level** *LEVEL*] [**--debug**] [**-q**|**--quiet**] [**-j**|**--jobs** *N*] [**--binary-diff** *METHOD*] [**--signature** *FILE*] *old-image new-image output*

**oci-delta apply** [**--ostree-repo** *PATH*] [**--directory** *PATH*] [**--container-storage** *PATH*] [**--verify-key** *FILE*] [**--log-level** *LEVEL*] [**--debug**] [**-q**|**--quiet**] *delta-file output*

**oci-delta import** [**--container-storage** *PATH*] [**-t**|**--tag** *NAME*] [**--verify-key** *FILE*] [**--log-level** *LEVEL*] [**--debug**] [**-q**|**--quiet**] *delta-file*

# DESCRIPTION

**oci-delta** creates and applies delta files between OCI image archives for bootc systems. Delta files are significantly smaller than full OCI archives because they exclude layers already present on the target system and use binary diffs (tar-diff) for changed layers.

The tool operates on OCI image archives and supports three modes of operation: creating deltas between two images, applying deltas to reconstruct full images, and directly importing deltas into container storage.

Delta files are OCI artifacts that can be stored in registries or distributed via removable media for air-gapped environments.

# SUBCOMMANDS

## create

Create a delta between two OCI images.

**Syntax:**

**oci-delta create** [*OPTIONS*] *old-image new-image output*

**Arguments:**

*old-image*
:   Old/source image. Supported formats: **oci-archive:***/PATH*, **oci:***/PATH*, or **containers-storage:***/REF*. If no type prefix is given, oci-archive is used.

*new-image*
:   New/target image (same format as old-image).

*output*
:   Output delta file (oci-archive:PATH or oci:PATH).

**Options:**

**-s**, **--statistics**
:   Show statistics after delta creation, including layer counts and bytes saved.

**--log-level**=*LEVEL*
:   Log verbosity level: **error**, **warn**, **info**, or **debug** (default: info).

**--debug**
:   Show detailed progress information during delta creation (shorthand for --log-level=debug).

**-q**, **--quiet**
:   Suppress all output except errors (shorthand for --log-level=error).

**-j**, **--jobs**=*N*
:   Maximum number of parallel tar-diff workers (default: number of CPUs).

**--binary-diff**=*METHOD*
:   Per-file binary diff backend: **bsdiff** (default), **zstd**, or **auto**. **auto** uses zstd under the zstd size cap, then bsdiff under the bsdiff cap.

**--compression-level**=*N*
:   Outer tar-diff zstd level (0 = tar-diff default).

**--zstd-diff-level**=*N*
:   zstd level for dictionary patches (-1 = use **--compression-level** / tar-diff default).

**--zstd-diff-window**=*N*
:   zstd window size in MiB for dictionary patches (0 = auto from source size, max 512).

**--max-zstd-diff-size**=*N*
:   Max file size in MiB for zstd dictionary patches (default: 128; 0 = no extra cap). Files over this cap skip zstd.

**--max-bsdiff-size**=*N*
:   Max file size in MiB for bsdiff (default: 192; 0 = no limit).

**--signature**=*FILE*
:   Signature OCI artifact to embed in the delta. Can be specified multiple times to embed multiple signatures. Enables offline signature verification during apply/import.

## apply

Apply a delta to reconstruct a full OCI image archive.

**Syntax:**

**oci-delta apply** [*OPTIONS*] *delta-file output*

**Arguments:**

*delta-file*
:   Path to the delta file to apply.

*output*
:   Output image (oci-archive:PATH or oci:PATH). If no type prefix is given, oci-archive is used.

**Options:**

**--ostree-repo**=*PATH*
:   OSTree repository path for delta source data (default: /ostree/repo). Auto-detects the source image ref via config digest. The --ostree-repo is the default on bootc systems.

**--directory**=*PATH*
:   Source directory containing layer files for delta reconstruction. Alternative to --ostree-repo.

**--container-storage**=*PATH*
:   Podman container storage root for delta reconstruction. Alternative to --ostree-repo.

**--verify-key**=*FILE*
:   Path to cosign public key in PEM format for signature verification. If specified, the delta's embedded signature will be verified before applying.

**--log-level**=*LEVEL*
:   Log verbosity level: **error**, **warn**, **info**, or **debug** (default: info).

**--debug**
:   Show detailed progress information during delta application (shorthand for --log-level=debug).

**-q**, **--quiet**
:   Suppress all output except errors (shorthand for --log-level=error).

Note: --ostree-repo, --directory, and --container-storage are mutually exclusive. Only one source type can be specified.

## import

Apply a delta and import the result directly into container storage.

**Syntax:**

**oci-delta import** [*OPTIONS*] *delta-file*

**Arguments:**

*delta-file*
:   Path to the delta file to import.

**Options:**

**--container-storage**=*PATH*
:   Podman container storage root (default: system default location).

**-t**, **--tag**=*NAME*
:   Tag name for the imported image (e.g., myimage:latest).

**--verify-key**=*FILE*
:   Path to cosign public key in PEM format for signature verification.

**--log-level**=*LEVEL*
:   Log verbosity level: **error**, **warn**, **info**, or **debug** (default: info).

**--debug**
:   Show detailed progress information during import (shorthand for --log-level=debug).

**-q**, **--quiet**
:   Suppress all output except errors (shorthand for --log-level=error).

After a successful import, the image ID is printed to stdout and can be used with podman or other container tools.

# EXIT STATUS

**0**
:   Success. The operation completed without errors.

**1**
:   An error occurred. Error details are written to stderr.

# EXAMPLES

**Create a delta between two OCI archives:**

```
$ oci-delta create old.oci-archive new.oci-archive update.oci-delta
```

**Create a delta with statistics:**

```
$ oci-delta create --statistics old.oci new.oci delta.oci

Delta creation statistics:
  Old image layers: 15
  New image layers: 16
  Processed layers: 8
  Skipped layers:   7
  Processed layer bytes:  125829120
  Tar-diff layer bytes:   8388608
  Original layer bytes:   12582912
  Bytes saved:            104857600 (83.3%)
```

**Apply a delta on a bootc system:**

```
$ oci-delta apply update.oci-delta new.oci-archive
$ bootc switch --transport=oci-archive new.oci-archive
$ rm new.oci-archive
```

**Import a delta directly into container storage:**

```
$ oci-delta import --tag myapp:v2.0 update.oci-delta
sha256:abc123...
$ podman images
REPOSITORY    TAG       IMAGE ID      CREATED        SIZE
myapp         v2.0      abc123...     1 minute ago   500 MB
```

**Create delta with embedded signature:**

```
$ oci-delta create --signature signature.oci \
    old.oci new.oci delta.oci
```

**Apply delta with signature verification:**

```
$ oci-delta apply --verify-key cosign.pub \
    signed-delta.oci output.oci
```

**Air-gapped USB workflow:**

```
# On internet-connected system: create delta
$ oci-delta create \
    containers-storage:registry.io/app:v1.0 \
    containers-storage:registry.io/app:v2.0 \
    /mnt/usb/app-update.delta

# Transfer USB drive to air-gapped system

# On air-gapped bootc system: apply and install
$ oci-delta apply /mnt/usb/app-update.delta app-v2.oci
$ bootc switch --transport=oci-archive app-v2.oci
$ reboot
```

**Use container storage as delta source:**

```
$ oci-delta apply --container-storage /var/lib/containers/storage \
    delta.oci output.oci
```

**Use custom directory as delta source:**

```
$ oci-delta apply --directory /path/to/layers \
    delta.oci output.oci
```

# FILES

*/ostree/repo*
:   Default OSTree repository path for delta source data on bootc systems. Layer files from installed images are stored as objects under this directory.

*/var/tmp/oci-delta-\**
:   Temporary working directories created during delta operations. Automatically cleaned up on successful completion.

*/var/lib/containers/storage*
:   Default podman container storage location (system default).

# REQUIREMENTS

The target system must run bootc version 1.15.0 or later for proper layer deduplication using diff_ids.

# SEE ALSO

**bootc**(8), **podman**(1), **ostree**(1), **cosign**(1)

Project homepage: <https://github.com/containers/oci-delta>

Delta file format specification: See README.md in the source repository.

# AUTHORS

**oci-delta** was written by the Containers organization.

See MAINTAINERS.md in the source repository for the current list of maintainers.

# COPYRIGHT

Copyright © 2026 the oci-delta authors.

Licensed under the Apache License, Version 2.0.
