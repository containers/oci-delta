#!/bin/bash
# Compare oci-delta create using bsdiff vs zstd binary-diff backends.
set -euo pipefail

usage() {
	echo "Usage: $0 <old-image> <new-image> [output-dir]"
	echo ""
	echo "Creates deltas with --binary-diff=bsdiff and --binary-diff=zstd,"
	echo "records wall time and output size, and runs analyze-delta.py on both."
	echo ""
	echo "Environment:"
	echo "  JOBS                 passed as -j (default: oci-delta default / GOMAXPROCS)"
	echo "  ZSTD_DIFF_LEVEL      --zstd-diff-level for the zstd run (default: 9)"
	echo "  ZSTD_DIFF_WINDOW     --zstd-diff-window MiB, 0=auto (default: 0)"
	echo "  ZSTD_DIFF_FALLBACK   --zstd-diff-fallback-raw true|false (default: true)"
	echo "  COMPRESSION_LEVEL    --compression-level for both runs (optional)"
	echo "  SKIP_ANALYZE         set to 1 to skip analyze-delta.py"
	echo ""
	echo "Arguments:"
	echo "  old-image   Old OCI image (path or typed ref)"
	echo "  new-image   New OCI image (path or typed ref)"
	echo "  output-dir  Directory for deltas and logs (default: mktemp)"
	exit 1
}

if [ "$#" -lt 2 ] || [ "$#" -gt 3 ]; then
	usage
fi

OLD_IMAGE="$1"
NEW_IMAGE="$2"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

if [ "$#" -eq 3 ]; then
	OUT_DIR="$3"
	mkdir -p "$OUT_DIR"
else
	OUT_DIR="$(mktemp -d -t oci-delta-compare.XXXXXX)"
	echo "Output directory: $OUT_DIR"
fi

OCI_DELTA="${OCI_DELTA:-$ROOT_DIR/oci-delta}"
if [ ! -x "$OCI_DELTA" ]; then
	echo "==> Building oci-delta"
	make -C "$ROOT_DIR" build
	OCI_DELTA="$ROOT_DIR/oci-delta"
fi

ANALYZE="$SCRIPT_DIR/analyze-delta.py"
JOBS="${JOBS:-}"
ZSTD_DIFF_LEVEL="${ZSTD_DIFF_LEVEL:-9}"
ZSTD_DIFF_WINDOW="${ZSTD_DIFF_WINDOW:-0}"
ZSTD_DIFF_FALLBACK="${ZSTD_DIFF_FALLBACK:-true}"
COMPRESSION_LEVEL="${COMPRESSION_LEVEL:-}"
SKIP_ANALYZE="${SKIP_ANALYZE:-0}"

run_create() {
	local method="$1"
	local out="$2"
	local log="$3"
	local -a cmd

	cmd=("$OCI_DELTA" create --binary-diff="$method" -v)
	if [ -n "$JOBS" ]; then
		cmd+=(-j "$JOBS")
	fi
	if [ -n "$COMPRESSION_LEVEL" ]; then
		cmd+=(--compression-level "$COMPRESSION_LEVEL")
	fi
	if [ "$method" = "zstd" ]; then
		cmd+=(--zstd-diff-level "$ZSTD_DIFF_LEVEL")
		cmd+=(--zstd-diff-window "$ZSTD_DIFF_WINDOW")
		cmd+=(--zstd-diff-fallback-raw="$ZSTD_DIFF_FALLBACK")
	fi
	cmd+=("$OLD_IMAGE" "$NEW_IMAGE" "$out")

	echo "==> ${cmd[*]}"
	TIMEFORMAT='%R'
	{
		echo "command: ${cmd[*]}"
		time "${cmd[@]}"
	} >"$log" 2>&1
}

BSDIFF_DELTA="$OUT_DIR/bsdiff.oci-delta"
ZSTD_DELTA="$OUT_DIR/zstd.oci-delta"
BSDIFF_LOG="$OUT_DIR/bsdiff.log"
ZSTD_LOG="$OUT_DIR/zstd.log"

run_create bsdiff "$BSDIFF_DELTA" "$BSDIFF_LOG"
run_create zstd "$ZSTD_DELTA" "$ZSTD_LOG"

bsdiff_secs="$(grep -E '^[0-9]+\.[0-9]+$' "$BSDIFF_LOG" | tail -1)"
zstd_secs="$(grep -E '^[0-9]+\.[0-9]+$' "$ZSTD_LOG" | tail -1)"
bsdiff_size="$(stat -c%s "$BSDIFF_DELTA")"
zstd_size="$(stat -c%s "$ZSTD_DELTA")"

echo ""
echo "==> Summary"
echo "zstd knobs: level=$ZSTD_DIFF_LEVEL window_mib=$ZSTD_DIFF_WINDOW fallback_raw=$ZSTD_DIFF_FALLBACK jobs=${JOBS:-default}"
printf '%-10s  %12s  %10s\n' "method" "time_sec" "size_bytes"
printf '%-10s  %12s  %10s\n' "bsdiff" "$bsdiff_secs" "$bsdiff_size"
printf '%-10s  %12s  %10s\n' "zstd" "$zstd_secs" "$zstd_size"

if [ -n "$bsdiff_secs" ] && [ -n "$zstd_secs" ] && awk "BEGIN {exit !($bsdiff_secs > 0)}"; then
	speedup="$(awk "BEGIN {printf \"%.2f\", $bsdiff_secs / $zstd_secs}")"
	size_pct="$(awk "BEGIN {printf \"%+.1f\", ($zstd_size - $bsdiff_size) * 100 / $bsdiff_size}")"
	echo "zstd speedup vs bsdiff: ${speedup}x"
	echo "zstd size vs bsdiff:    ${size_pct}%"
fi

echo ""
echo "==> create -v stats"
grep -E 'Tar-diff layer|Original layer|Bytes saved' "$BSDIFF_LOG" "$ZSTD_LOG" || true

if [ "$SKIP_ANALYZE" != "1" ]; then
	echo ""
	echo "==> analyze-delta.py (bsdiff)"
	python3 "$ANALYZE" "$BSDIFF_DELTA" | tee "$OUT_DIR/bsdiff.analyze.txt"

	echo ""
	echo "==> analyze-delta.py (zstd)"
	python3 "$ANALYZE" "$ZSTD_DELTA" | tee "$OUT_DIR/zstd.analyze.txt"
fi

echo ""
echo "Logs and deltas in: $OUT_DIR"
