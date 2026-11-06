#!/usr/bin/env bash
#
# Generates the files that the deb and rpm packages install besides the
# binary: the compressed man page and the shell completion scripts. Run by
# GoReleaser (the before hook of .goreleaser.yaml) into a git-ignored
# directory, so that the work tree stays clean.
#
#   script/package-files.sh [OUTDIR]
#
# OUTDIR defaults to build/package. The output is reproducible: gzip -n
# records neither the file name nor a time.
set -euo pipefail

cd "$(dirname "$0")/.."
out=${1:-build/package}

rm -rf "$out"
mkdir -p "$out/completions"
gzip -9 -n -c documentation/droot.1 >"$out/droot.1.gz"

# The completion scripts do not depend on the platform droot is built for.
bin=$(mktemp -d "${TMPDIR:-/tmp}/droot-package.XXXXXX")
trap 'rm -rf "$bin"' EXIT
CGO_ENABLED=0 go build -o "$bin/droot" ./cmd/droot
"$bin/droot" completion bash >"$out/completions/droot.bash"
"$bin/droot" completion zsh >"$out/completions/_droot"
"$bin/droot" completion fish >"$out/completions/droot.fish"
