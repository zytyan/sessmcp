#!/bin/sh
set -eu
mkdir -p dist
for target in linux/amd64 linux/arm64 windows/amd64; do
  target_os=${target%/*}
  target_arch=${target#*/}
  suffix=
  if [ "$target_os" = windows ]; then suffix=.exe; fi
  CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" go build -buildvcs=false -trimpath -ldflags='-s -w' -o "dist/sessmcp-$target_os-$target_arch$suffix" ./cmd/sessmcp
done
(cd dist && sha256sum sessmcp-* > SHA256SUMS)
