#!/bin/sh
set -eu
version="${1:?需要版本标签}"
commit="$(git rev-parse HEAD)"
mkdir -p dist
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
  target_os="${target%/*}"
  target_arch="${target#*/}"
  directory="dist/cloudlet_${target_os}_${target_arch}"
  mkdir -p "$directory"
  for program in server cloudlet; do
    name=cloudlet
    if [ "$program" = server ]; then name=cloudlet-server; fi
    CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" go build -trimpath \
      -ldflags "-s -w -X github.com/wendaining/cloudlet/internal/version.Version=$version -X github.com/wendaining/cloudlet/internal/version.Commit=$commit" \
      -o "$directory/$name" "./cmd/$program"
  done
  cp LICENSE README.md AGENTS.md CONTRIBUTING.md "$directory/"
  cp -R configs docs "$directory/"
  tar -czf "$directory.tar.gz" -C "$directory" .
done
cd dist
shasum -a 256 ./*.tar.gz > SHA256SUMS
