#!/usr/bin/env bash
# Usage: scripts/npm-pack.sh <outdir>
# Builds the npm packages from goreleaser's dist/: one package per platform holding its binary, and
# postgres-dba-mcp, a launcher that lists them all as optional dependencies so npm installs the one matching the
# host. Each package is written as a directory under <outdir>, ready for `npm publish`. Publish the launcher last.
set -euo pipefail

out=$1
version=$(jq -r .version dist/metadata.json)
rm -rf "$out"
mkdir -p "$out"

deps='{}'
while IFS=$'\t' read -r goos goarch path; do
  # npm names platforms the way Node does: process.platform and process.arch.
  os=$goos cpu=$goarch
  [ "$goos" = windows ] && os=win32
  [ "$goarch" = amd64 ] && cpu=x64
  name=postgres-dba-mcp-$os-$cpu
  mkdir -p "$out/$name/bin"
  cp "$path" "$out/$name/bin/"
  cp LICENSE NOTICE "$out/$name/"
  jq -n --arg name "$name" --arg version "$version" --arg os "$os" --arg cpu "$cpu" '{
    name: $name, version: $version,
    description: "The postgres-dba-mcp binary for \($os) \($cpu). Install postgres-dba-mcp instead.",
    license: "MIT",
    repository: {type: "git", url: "git+https://github.com/a-lafon/postgres-dba-mcp.git"},
    os: [$os], cpu: [$cpu], files: ["bin", "NOTICE"]
  }' >"$out/$name/package.json"
  deps=$(jq -c --arg name "$name" --arg version "$version" '.[$name] = $version' <<<"$deps")
done < <(jq -r '.[] | select(.type == "Binary") | [.goos, .goarch, .path] | @tsv' dist/artifacts.json)

# The five platforms of .goreleaser.yaml: fewer means dist/artifacts.json changed shape.
test "$(jq length <<<"$deps")" -eq 5 || { echo "expected 5 binaries in dist/artifacts.json, got: $deps" >&2; exit 1; }

mkdir "$out/postgres-dba-mcp"
cp -r npm/bin README.md LICENSE NOTICE "$out/postgres-dba-mcp/"
jq --arg version "$version" --argjson deps "$deps" '.version = $version | .optionalDependencies = $deps' \
  npm/package.json >"$out/postgres-dba-mcp/package.json"
