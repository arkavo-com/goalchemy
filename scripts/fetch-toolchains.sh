#!/usr/bin/env bash
# Fetch the pinned Linux x86_64 toolchains into .toolchains/.
set -euo pipefail

repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
# shellcheck disable=SC1091
source "$repo/toolchains.lock"
tc="$repo/.toolchains"
mkdir -p "$tc/downloads"

if [[ $(uname -s) != Linux || $(uname -m) != x86_64 ]]; then
  echo 'toolchains.lock supports Linux x86_64 only' >&2
  exit 2
fi

only=${1:-all}
case "$only" in all|jdk|dotnet|bdwgc|llvm|swift|tinygo|binaryen) ;; *) echo "unknown toolchain: $only" >&2; exit 2 ;; esac

fetch() {
  local url=$1 hash=$2 archive=$3
  if [[ -f $archive ]] && ! printf '%s  %s\n' "$hash" "$archive" | sha256sum -c --status; then
    rm "$archive"
  fi
  if [[ ! -f $archive ]]; then
    local partial="$archive.partial"
    curl -fL --retry 2 --retry-all-errors --retry-delay 2 --continue-at - --output "$partial" "$url"
    mv "$partial" "$archive"
  fi
  printf '%s  %s\n' "$hash" "$archive" | sha256sum -c
}

install_tar() {
  local archive=$1 dest=$2 strip=$3 expected=$4 hash=$5
  if [[ -x $expected && -f $dest/.goalchemy-sha256 ]] &&
      [[ $(cat "$dest/.goalchemy-sha256") == "$hash" ]]; then
    return
  fi
  local staging
  staging=$(mktemp -d "$tc/.install.XXXXXXXX")
  if [[ $strip == 1 ]]; then
    tar -xf "$archive" -C "$staging" --strip-components=1 --no-same-owner
  else
    tar -xf "$archive" -C "$staging" --no-same-owner
  fi
  if [[ ! -x $staging/${expected#"$dest"/} ]]; then
    echo "archive lacks $expected" >&2
    rm -rf "$staging"
    exit 1
  fi
  printf '%s\n' "$hash" > "$staging/.goalchemy-sha256"
  rm -rf "$dest"
  mv "$staging" "$dest"
}

if [[ $only == all || $only == jdk ]]; then
  archive="$tc/downloads/OpenJDK21U-jdk_x64_linux_hotspot_21.0.12.1_1.tar.gz"
  fetch "$JDK_URL" "$JDK_SHA256" "$archive"
  install_tar "$archive" "$tc/jdk-$JDK_VERSION" 1 "$tc/jdk-$JDK_VERSION/bin/javac" "$JDK_SHA256"
fi

if [[ $only == all || $only == dotnet ]]; then
  archive="$tc/downloads/dotnet-sdk-$DOTNET_VERSION-linux-x64.tar.gz"
  fetch "$DOTNET_URL" "$DOTNET_SHA256" "$archive"
  install_tar "$archive" "$tc/dotnet" 0 "$tc/dotnet/dotnet" "$DOTNET_SHA256"
fi

if [[ $only == all || $only == bdwgc ]]; then
  archive="$tc/downloads/gc-$BDWGC_VERSION.tar.gz"
  fetch "$BDWGC_URL" "$BDWGC_SHA256" "$archive"
  if [[ ! -f $tc/bdwgc/lib/libgc.a || ! -f $tc/bdwgc/.goalchemy-sha256 ]] ||
      [[ $(cat "$tc/bdwgc/.goalchemy-sha256" 2>/dev/null || true) != "$BDWGC_SHA256" ]]; then
    build=$(mktemp -d "$tc/.build.XXXXXXXX")
    prefix=$(mktemp -d "$tc/.install.XXXXXXXX")
    tar -xf "$archive" -C "$build" --strip-components=1 --no-same-owner
    (cd "$build" && ./configure --prefix="$prefix" --enable-threads=posix \
      --with-libatomic-ops=none --disable-docs --enable-static --disable-shared --quiet \
      && make -j "${JOBS:-2}" && make install)
    printf '%s\n' "$BDWGC_SHA256" > "$prefix/.goalchemy-sha256"
    rm -rf "$tc/bdwgc"
    mv "$prefix" "$tc/bdwgc"
    rm -rf "$build"
  fi
fi

if [[ $only == all || $only == llvm ]]; then
  archive="$tc/downloads/clang+llvm-$LLVM_VERSION-x86_64-linux-gnu-ubuntu-18.04.tar.xz"
  fetch "$LLVM_URL" "$LLVM_SHA256" "$archive"
  install_tar "$archive" "$tc/llvm-$LLVM_VERSION" 1 "$tc/llvm-$LLVM_VERSION/bin/clang" "$LLVM_SHA256"
  archive="$tc/downloads/libtinfo5_${LIBTINFO5_VERSION}_amd64.deb"
  fetch "$LIBTINFO5_URL" "$LIBTINFO5_SHA256" "$archive"
  if [[ ! -f $tc/libtinfo5/lib/x86_64-linux-gnu/libtinfo.so.5 ||
        ! -f $tc/libtinfo5/.goalchemy-sha256 ]] ||
      [[ $(cat "$tc/libtinfo5/.goalchemy-sha256" 2>/dev/null || true) != "$LIBTINFO5_SHA256" ]]; then
    staging=$(mktemp -d "$tc/.install.XXXXXXXX")
    dpkg-deb -x "$archive" "$staging"
    printf '%s\n' "$LIBTINFO5_SHA256" > "$staging/.goalchemy-sha256"
    rm -rf "$tc/libtinfo5"
    mv "$staging" "$tc/libtinfo5"
  fi
fi

if [[ $only == all || $only == swift ]]; then
  archive="$tc/downloads/swift-$SWIFT_VERSION-RELEASE-ubuntu22.04.tar.gz"
  fetch "$SWIFT_URL" "$SWIFT_SHA256" "$archive"
  install_tar "$archive" "$tc/swift-$SWIFT_VERSION" 1 "$tc/swift-$SWIFT_VERSION/usr/bin/swiftc" "$SWIFT_SHA256"
fi

if [[ $only == all || $only == tinygo ]]; then
  archive="$tc/downloads/tinygo$TINYGO_VERSION.linux-amd64.tar.gz"
  fetch "$TINYGO_URL" "$TINYGO_SHA256" "$archive"
  install_tar "$archive" "$tc/tinygo" 1 "$tc/tinygo/bin/tinygo" "$TINYGO_SHA256"
fi

if [[ $only == all || $only == tinygo || $only == binaryen ]]; then
  archive="$tc/downloads/binaryen-version_$BINARYEN_VERSION-x86_64-linux.tar.gz"
  fetch "$BINARYEN_URL" "$BINARYEN_SHA256" "$archive"
  install_tar "$archive" "$tc/binaryen" 1 "$tc/binaryen/bin/wasm-opt" "$BINARYEN_SHA256"
fi

echo "toolchains ready under $tc"
