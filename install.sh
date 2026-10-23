#!/bin/sh
# Installs the postgres-dba-mcp binary from a GitHub release, without Homebrew:
#
#   curl -fsSL https://raw.githubusercontent.com/a-lafon/postgres-dba-mcp/main/install.sh | sh
#
# It downloads the archive for this OS and architecture, checks it against the
# release's checksums.txt, and puts postgres-dba-mcp in
# POSTGRES_DBA_MCP_INSTALL_DIR (default ~/.local/bin). POSTGRES_DBA_MCP_VERSION
# picks a release tag, such as v0.1.0 (default: the latest).
#
# A file fetched with curl or wget is not quarantined by macOS, so the unsigned
# binary runs without clearing that mark.

set -eu

repo=a-lafon/postgres-dba-mcp
bin=postgres-dba-mcp

fail() {
	echo "$bin install: $*" >&2
	exit 1
}

# fetch URL FILE downloads with curl or, without it, wget (busybox included).
fetch() {
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL -o "$2" "$1"
	elif command -v wget >/dev/null 2>&1; then
		wget -q -O "$2" "$1"
	else
		fail "needs curl or wget"
	fi
}

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d ' ' -f 1
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | cut -d ' ' -f 1
	else
		fail "needs sha256sum or shasum to check the download"
	fi
}

# The whole script runs from main, so a download cut short runs nothing.
main() {
	case $(uname -s) in
	Darwin) os=darwin ;;
	Linux) os=linux ;;
	*) fail "no install script for $(uname -s): download the archive from https://github.com/$repo/releases" ;;
	esac
	case $(uname -m) in
	x86_64 | amd64) arch=amd64 ;;
	arm64 | aarch64) arch=arm64 ;;
	*) fail "no release for $(uname -m)" ;;
	esac

	version=${POSTGRES_DBA_MCP_VERSION:-latest}
	if [ "$version" = latest ]; then
		base=https://github.com/$repo/releases/latest/download
	else
		base=https://github.com/$repo/releases/download/$version
	fi
	dir=${POSTGRES_DBA_MCP_INSTALL_DIR:-$HOME/.local/bin}

	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT

	fetch "$base/checksums.txt" "$tmp/checksums.txt" || fail "cannot download $base/checksums.txt"
	# The archive's name holds the version, so it is read from checksums.txt:
	# "latest" then needs no API call to know which version it is.
	line=$(grep "_${os}_${arch}\.tar\.gz\$" "$tmp/checksums.txt") || fail "no ${os}_${arch} archive in $base/checksums.txt"
	want=${line%% *}
	archive=${line##* }

	fetch "$base/$archive" "$tmp/$archive" || fail "cannot download $base/$archive"
	got=$(sha256 "$tmp/$archive")
	[ "$got" = "$want" ] || fail "$archive does not match checksums.txt: got $got, want $want"

	tar -xzf "$tmp/$archive" -C "$tmp" "$bin"
	mkdir -p "$dir"
	# Copied next to its target, then renamed: a rename within one directory
	# never leaves a half-written binary for a client to start.
	cp "$tmp/$bin" "$dir/.$bin.new"
	chmod 755 "$dir/.$bin.new"
	mv -f "$dir/.$bin.new" "$dir/$bin"

	echo "Installed $("$dir/$bin" --version) in $dir/$bin"
	case ":$PATH:" in
	*":$dir:"*) ;;
	*) echo "$dir is not on your PATH: add it, or register the server with its full path $dir/$bin." ;;
	esac
}

main
