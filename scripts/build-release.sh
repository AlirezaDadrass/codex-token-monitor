#!/usr/bin/env bash
# Build portable native binaries with the host Go toolchain. No containers.
set -euo pipefail
version="${1:-v0.1.4}"
unix_only=false
if [[ "${2:-}" == --unix-only ]]; then unix_only=true; elif [[ -n "${2:-}" ]]; then printf '%s\n' 'Unknown build option' >&2; exit 1; fi
[[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { printf '%s\n' 'Use a version such as v0.1.4' >&2; exit 1; }
repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_dir"
for command in go tar sha256sum; do command -v "$command" >/dev/null || { printf 'Missing %s\n' "$command" >&2; exit 1; }; done
if ! $unix_only; then command -v zip >/dev/null || { printf '%s\n' 'Missing zip' >&2; exit 1; }; fi
mkdir -p dist .local
stage_dir="$(mktemp -d "$repo_dir/.local/release-build-XXXXXX")"
trap 'rm -rf -- "$stage_dir"' EXIT
archives=()
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
  platform="${target%/*}"
  if $unix_only && [[ "$platform" == windows ]]; then continue; fi
  arch="${target#*/}"
  executable=token-monitor
  [[ "$platform" == windows ]] && executable=token-monitor.exe
  printf 'Building %s\n' "$target"
  env CGO_ENABLED=0 GOOS="$platform" GOARCH="$arch" go build -trimpath -ldflags='-s -w' -o "$stage_dir/$executable" ./cmd/token-monitor
  cp README.md THIRD_PARTY_NOTICES.md THIRD_PARTY_LICENSES.txt "$stage_dir/"
  cp internal/monitor/templates/OFL.txt "$stage_dir/OFL.txt"
  cp scripts/install-windows.ps1 "$stage_dir/install-windows.ps1"
  mkdir -p "$stage_dir/docs"
  cp -R docs/. "$stage_dir/docs/"
  stem="token-monitor_${version#v}_${platform}_${arch}"
  if [[ "$platform" == windows ]]; then
    archive="$stem.zip"
    (cd "$stage_dir" && zip -qr "$repo_dir/dist/$archive" "$executable" README.md THIRD_PARTY_NOTICES.md THIRD_PARTY_LICENSES.txt OFL.txt install-windows.ps1 docs)
  else
    archive="$stem.tar.gz"
    tar -czf "$repo_dir/dist/$archive" -C "$stage_dir" "$executable" README.md THIRD_PARTY_NOTICES.md THIRD_PARTY_LICENSES.txt OFL.txt install-windows.ps1 docs
  fi
  archives+=("$archive")
  rm -- "$stage_dir/$executable"
done
cp scripts/install-windows.ps1 dist/install-windows.ps1
archives+=(install-windows.ps1)
(cd dist && sha256sum "${archives[@]}" > checksums.txt)
printf '%s\n' 'Release archives and checksums are in dist/.'
