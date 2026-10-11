#!/bin/sh
# Independent structural checks compile the real runtime with only the stdlib.
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
out=$(mktemp -d)
trap 'rm -rf "$out"' EXIT
mkdir -p "$out/rt"
cp -r "$root/types" "$root/runtime" "$out/rt/"
cp "$root/tests/host_operations_test.rs" "$out/main.rs"
{
  for f in "$out"/rt/types/*.rs "$out"/rt/runtime/*.rs; do
    d=$(basename "$(dirname "$f")"); n=$(basename "$f" .rs)
    case "$n" in lib_crypto_*|lib_encoding_*|lib_http_*|lib_callback_request|lib_checksum_crc32_ieee) continue ;; esac
    printf '#[path = "%s/%s.rs"]\npub mod %s;\npub use %s::*;\n' "$d" "$n" "$n" "$n"
  done
} > "$out/rt/mod.rs"
rustc --edition 2021 -Awarnings --test -C opt-level=1 -o "$out/test" "$out/main.rs"
GOALCHEMY_GC_THRESHOLD=1 "$out/test" --nocapture --test-threads=1
