#!/bin/sh
# Each diagnostic runs in a new process; all sources/results remain retained.
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
out=${1:-out/rust-byte-bench}
mkdir -p "$out/rt"
cp -r "$root/types" "$root/runtime" "$out/rt/"
cp "$root/tests/byte_storage_bench.rs" "$out/main.rs"
{
  for f in "$out"/rt/types/*.rs "$out"/rt/runtime/*.rs; do
    d=$(basename "$(dirname "$f")"); n=$(basename "$f" .rs)
    case "$n" in lib_crypto_*|lib_encoding_*|lib_http_*|lib_callback_request|lib_checksum_crc32_ieee) continue ;; esac
    printf '#[path = "%s/%s.rs"]\npub mod %s;\npub use %s::*;\n' "$d" "$n" "$n" "$n"
  done
} > "$out/rt/mod.rs"
rustc --version
cargo --version
rustc --edition 2021 -Awarnings -C opt-level=2 -C debuginfo=0 -o "$out/bench" "$out/main.rs"
for size in 2 8; do
  for mode in native generic; do
    /usr/bin/time -f 'process_elapsed_s=%e max_rss_kib=%M' "$out/bench" "$mode" "$size"
  done
done
