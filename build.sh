#!/usr/bin/env bash
# build.sh builds ektara, and builds it small.
#
# The -s -w ldflags are the point of this script. Go puts debug info in a
# binary by default, and on this program it is bigger than the program:
# 19.2MB normally, 12.4MB with them, which is a third off for no change in
# behaviour. -s throws away the symbol table and -w throws away DWARF, and
# neither is needed to run it. -trimpath drops the full paths of the machine
# that built it, which is both a little smaller and a little less to leak.
#
# The version and commit are stamped in from git, so a tag is the only thing
# that changes what --version says. --always keeps the version readable when
# the tree has no tags yet: it falls back to the commit hash instead of
# printing nothing.
#
# Usage:  ./build.sh          build ./ektara
#         ./build.sh run      build it, then run it

set -euo pipefail
cd "$(dirname "$0")"

out=ektara
version=$(git describe --tags --always --dirty)
commit=$(git rev-parse --short HEAD)
flags=(-trimpath -ldflags="-s -w -X main.version=$version -X main.commit=$commit")

echo "building $out ..."
go build "${flags[@]}" -o "$out" .

size=$(stat -c %s "$out")
mb=$(awk "BEGIN{printf \"%.1f\", $size/1048576}")
printf 'built %s  (%s bytes, %s MB)\n' "$out" "$size" "$mb"

if [ "${1:-}" = "run" ]; then
	exec "$out"
fi
