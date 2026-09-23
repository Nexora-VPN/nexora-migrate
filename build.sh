#!/bin/sh
# Build for this machine. That is all it takes:
#
#     ./build.sh
#
# No cgo, no C toolchain, no npm. To build for somewhere else, set GOOS and
# GOARCH — a Windows binary from a Mac is:
#
#     GOOS=windows GOARCH=amd64 ./build.sh
#
set -e
VERSION="${VERSION:-dev}"
OUT="nexora-migrate"
[ "${GOOS:-}" = "windows" ] && OUT="nexora-migrate.exe"

CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$OUT" .
echo "built $OUT ($(du -h "$OUT" | cut -f1))"
echo "run it with:  ./$OUT"
