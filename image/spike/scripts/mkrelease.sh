#!/bin/sh
# Build one release set (root.erofs + root.verity + roothash) from the extracted base tree.
# Usage: mkrelease.sh <label> [shell snippet run with CWD=tree copy, e.g. 'echo B > etc/nex-slot']
# Fixed timestamp/UUID/salt so the same tree gives the same roothash. Runs inside the tools container.
set -eu
label=$1; mod=${2:-}
SDE=1790000000
out=/lab/rel/$label; rm -rf "$out" /lab/work/reltree; mkdir -p "$out"
cp -a /lab/work/tree /lab/work/reltree
(cd /lab/work/reltree && eval "$mod")
mkfs.erofs -T $SDE -U 11111111-2222-3333-4444-555555555555 $EROFS_OPTS "$out/root.erofs" /lab/work/reltree >/dev/null 2>&1
rm -rf /lab/work/reltree
veritysetup format --uuid=22222222-3333-4444-5555-666666666666 \
  --salt=0000000000000000000000000000000000000000000000000000000000000001 \
  "$out/root.erofs" "$out/root.verity" | awk '/^Root hash:/{print $3}' > "$out/roothash"
echo "$label $(cat "$out/roothash") erofs=$(stat -c %s "$out/root.erofs") verity=$(stat -c %s "$out/root.verity")"
chown -R 1000:1000 "$out"
