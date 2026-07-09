#!/bin/sh

set -eu

tag=${1-}
case "$tag" in
  v*) version=${tag#v} ;;
  *)
    printf 'usage: %s vVERSION\n' "$0" >&2
    exit 2
    ;;
esac

root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
dist="$root/dist"
rm -rf "$dist"
mkdir -p "$dist"

build() {
  goos=$1
  goarch=$2
  extension=$3
  base="updog_${version}_${goos}_${goarch}"
  work="$dist/$base"
  binary=updog
  if [ "$goos" = windows ]; then
    binary=updog.exe
  fi

  mkdir -p "$work"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath -ldflags "-s -w -X main.version=$version" \
    -o "$work/$binary" ./cmd/updog

  if [ "$extension" = zip ]; then
    (cd "$work" && zip -q "$dist/$base.zip" "$binary")
  else
    tar -C "$work" -czf "$dist/$base.tar.gz" "$binary"
  fi
  rm -rf "$work"
}

cd "$root"
build darwin amd64 tar
build darwin arm64 tar
build linux amd64 tar
build linux arm64 tar
build windows amd64 zip
build windows arm64 zip

(cd "$dist" && sha256sum ./*.tar.gz ./*.zip > SHA256SUMS)
