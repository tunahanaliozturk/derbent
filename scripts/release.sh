#!/usr/bin/env bash
# Builds every release target twice in separate directories and fails if any pair differs, then
# writes the binaries and SHA256SUMS to dist/. Usage: scripts/release.sh <version>
#
# The second build of each pair compiles from its own empty cache, so a match shows the compiler
# output is stable too, not only the link. Both builds stamp the same VCS information (revision,
# commit time, whether the tree is modified), since they run from the same checkout and dist/ is
# ignored by git. A tag rebuilt from a clean checkout of that tag, with go1.27.1
# (GOTOOLCHAIN=go1.27.1) and no GOFLAGS, GOAMD64 or GOARM64 overrides, therefore gives the
# published checksums.
set -euo pipefail
version="${1:?usage: scripts/release.sh <version>}"
cd "$(dirname -- "${BASH_SOURCE[0]}")/.."
[ -f go.mod ] && [ -d cmd/derbent ] || { echo "release.sh: run it from the derbent repository" >&2; exit 1; }
targets="windows/amd64 windows/arm64 linux/amd64 linux/arm64 darwin/amd64 darwin/arm64"
rm -rf dist && mkdir -p dist/a dist/b
for t in $targets; do
  os="${t%/*}"; arch="${t#*/}"
  ext=""; [ "$os" = windows ] && ext=".exe"
  name="derbent-${version}-${os}-${arch}${ext}"
  for d in a b; do
    cache="$(go env GOCACHE)"; [ "$d" = b ] && cache="$PWD/dist/b/cache"
    GOCACHE="$cache" CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
      -ldflags "-s -w -buildid= -X main.version=${version}" -o "dist/${d}/${name}" ./cmd/derbent
  done
  if ! cmp -s "dist/a/${name}" "dist/b/${name}"; then
    echo "not reproducible: ${name}" >&2
    exit 1
  fi
  mv "dist/a/${name}" "dist/${name}"
done
rm -rf dist/a dist/b
(cd dist && sha256sum derbent-* > SHA256SUMS)
cat dist/SHA256SUMS
