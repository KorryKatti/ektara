#!/bin/sh
# install.sh builds and installs ektara from source.
#
# It is meant to be fetched and run in one line:
#
#   curl -fsSL https://raw.githubusercontent.com/korrykatti/ektara/main/install.sh | sh
#
# It checks for the things a build needs, says how to get anything that is
# missing, then builds ektara and puts it in ~/.local/bin. No sudo, and nothing
# outside your home directory is written.

set -eu

repo=korrykatti/ektara
branch=main
bindir=${BINDIR:-$HOME/.local/bin}

tmpdir=""
tmpbin=""
cleanup() {
	if [ -n "$tmpdir" ]; then rm -rf "$tmpdir"; fi
	if [ -n "$tmpbin" ]; then rm -f "$tmpbin"; fi
}
trap cleanup EXIT

have() { command -v "$1" >/dev/null 2>&1; }

# One line per missing thing. The reader is in the middle of a pipe, so the
# hint has to be something they can paste, not a paragraph.
missing=0
need() {
	printf 'ektara needs %s to build. install it with:\n  %s\n' "$1" "$2" >&2
	missing=1
}

have go || need "Go 1.27+ (https://go.dev/dl/)" "apt install golang  /  pacman -S go  /  dnf install golang"
have cc || have gcc || have clang || need "a C compiler, for the database" "apt install gcc  /  pacman -S gcc  /  dnf install gcc"

# The build uses -tags nocgo, which opens libmpv when the program starts rather
# than linking it, so only the shared library is needed and not the -dev
# headers. Three ways of finding it, because none of them is everywhere.
if ! ldconfig -p 2>/dev/null | grep -q 'libmpv\.so' \
	&& ! pkg-config --exists mpv 2>/dev/null \
	&& ! have mpv; then
	need "libmpv" "apt install libmpv2  /  pacman -S mpv  /  dnf install mpv-libs"
fi

# ffmpeg is only needed to download songs. Streaming and your own files work
# without it, so this is a note rather than a stop.
have ffmpeg || printf 'note: ffmpeg is missing, so downloading songs will not work.\n' >&2

if [ "$missing" -ne 0 ]; then
	exit 1
fi

# Build from this checkout when the script sits in one. That is also what makes
# the script testable without a network: outside a checkout it fetches the
# source into a temporary directory.
if [ -f go.mod ] && grep -qx 'module ektara' go.mod 2>/dev/null; then
	src=$PWD
else
	if ! have curl && ! have wget; then
		printf 'ektara needs curl or wget to download its source.\n' >&2
		exit 1
	fi
	tmpdir=$(mktemp -d)
	printf 'downloading %s ...\n' "$repo"
	if have curl; then
		curl -fsSL "https://github.com/$repo/archive/refs/heads/$branch.tar.gz" | tar -xz -C "$tmpdir"
	else
		wget -qO- "https://github.com/$repo/archive/refs/heads/$branch.tar.gz" | tar -xz -C "$tmpdir"
	fi
	src=$tmpdir/ektara-$branch
fi

# Stamp the version the same way build.sh does, when there is a git tree to ask.
ldflags="-s -w"
if [ -d "$src/.git" ] && have git; then
	version=$(cd "$src" && git describe --tags --always --dirty)
	commit=$(cd "$src" && git rev-parse --short HEAD)
	ldflags="-s -w -X main.version=$version -X main.commit=$commit"
fi

mkdir -p "$bindir"
tmpbin="$bindir/.ektara.$$"

printf 'building ektara ...\n'
(cd "$src" && CGO_ENABLED=1 go build -tags nocgo -trimpath -ldflags "$ldflags" -o "$tmpbin" .)

# Rename rather than building straight onto the installed path, so a build that
# fails halfway does not leave a broken binary behind.
mv "$tmpbin" "$bindir/ektara"
tmpbin=""

printf 'installed %s\n' "$bindir/ektara"
"$bindir/ektara" --version

case ":$PATH:" in
*":$bindir:"*) ;;
*)
	printf '\n%s is not on your PATH. Add this to your shell profile:\n  export PATH="%s:$PATH"\n' "$bindir" "$bindir"
	;;
esac
