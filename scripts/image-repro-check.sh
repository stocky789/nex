#!/bin/sh
# Double-build the Nex Core image (and optionally its layers) from the same pinned inputs and record every difference.
#
#   scripts/image-repro-check.sh [--layers "nex-zfs"] [--parallel] [--no-base --base-tree DIR] [--report DIR]
#
# What is held equal between run 1 and run 2:
#   * the source: the repository inputs (tracked and untracked, not ignored files) are copied once to
#     <report>/src and BOTH runs build from that copy, so an edit made in the work tree during the check
#     cannot show up as a difference; <report>/inputs.txt records the digest of the copy;
#   * SOURCE_DATE_EPOCH (default 1790000000), NEX_LABEL/NEX_SEQUENCE, the mkosi Seed (image/mkosi.conf),
#     the package snapshot (image/snapshot), the tools container, NEX_PACKAGE_DIRS and the module signing key;
#   * each run has its own output, cache, sandbox and work directory (nothing is carried from run 1 to run 2).
#
# Default: image/build.sh base then release (roothash, root.erofs, root.verity, kernel, initrd, base tree,
# UKI, ESP tree, manifest), twice. --layers adds image/mkosi.images/build-layer.sh for each named layer.
# --no-base compares layers only: --base-tree DIR is a finished base output both runs overlay (lab use).
# --parallel runs the two builds at the same time (they share only the tools image and the network).
# Containers are uncapped unless NEX_DOCKER_CPUS / NEX_DOCKER_MEMORY are set.
#
# The report (default image/out/repro/) holds summary.tsv (artifact, digest run 1, digest run 2, equal),
# inputs.txt, run1.log/run2.log and, for every artifact that differs, diff/<artifact>.files.txt (the diff of the two
# manifests of path, mode, owner, type, size, mtime, link target and per-file sha256) plus a diffoscope text report
# per differing file or artifact, made in the pinned tools container as root so root-only files are compared too.
# A single-file artifact's digest is its sha256. Exit 0 only when every artifact is byte-identical: an identical
# roothash is a hard gate before the first external release.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
repo=$(dirname "$here")
layers=""
do_base=1
parallel=0
base_tree=
report=$repo/image/out/repro
while [ $# -gt 0 ]; do
    case $1 in
        --layers) layers=$2; shift 2 ;;
        --no-base) do_base=0; shift ;;
        --base-tree) base_tree=$2; shift 2 ;;
        --parallel) parallel=1; shift ;;
        --report) report=$2; shift 2 ;;
        *) echo "unknown argument: $1" >&2; exit 2 ;;
    esac
done
if [ "$do_base" = 0 ] && [ -z "$base_tree" ]; then
    echo "--no-base needs --base-tree DIR" >&2
    exit 2
fi
if [ "$do_base" = 0 ] && [ -z "$layers" ]; then
    echo "--no-base needs --layers" >&2
    exit 2
fi

mkdir -p "$report"
report=$(cd "$report" && pwd)
snapshot=$(cat "$repo/image/snapshot")
docker=${NEX_DOCKER:-docker}
tools=${NEX_TOOLS_IMAGE:-nex-image-tools}
if [ -z "${NEX_TOOLS_IMAGE:-}" ]; then
    $docker build -q -t "$tools" --build-arg "SNAPSHOT=$snapshot" "$repo/image/tools" >/dev/null
fi
# Every container runs under the same optional caps as image/build.sh.
caps="${NEX_DOCKER_CPUS:+--cpus $NEX_DOCKER_CPUS} ${NEX_DOCKER_MEMORY:+--memory $NEX_DOCKER_MEMORY}"
# Root-owned products of an earlier check can only be removed from inside the container.
# shellcheck disable=SC2086
$docker run --rm $caps -v "$report":/r "$tools" sh -c 'rm -rf /r/run1 /r/run2 /r/diff /r/src /r/summary.tsv /r/inputs.txt /r/run1.log /r/run2.log'
mkdir -p "$report/diff"

src=$report/src
mkdir -p "$src"
(cd "$repo" && git ls-files -co --exclude-standard -z) | rsync -a --from0 --files-from=- --ignore-missing-args "$repo"/ "$src"/
srcdigest=$(cd "$src" && find . -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum | sha256sum | cut -d' ' -f1)

export SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH:-1790000000}
export NEX_LABEL=${NEX_LABEL:-repro}
export NEX_SEQUENCE=${NEX_SEQUENCE:-1}
{
    echo "date_utc	$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo "source_sha256	$srcdigest"
    echo "snapshot	$snapshot"
    echo "source_date_epoch	$SOURCE_DATE_EPOCH"
    echo "label_sequence	$NEX_LABEL $NEX_SEQUENCE"
    echo "tools_image_id	$($docker image inspect -f '{{.Id}}' "$tools")"
    echo "go	$(go version)"
    echo "layers	${layers:-none}"
    echo "parallel	$parallel"
} > "$report/inputs.txt"

srcache=${NEX_MKOSI_CACHE:-$repo/image/out/cache}

build_one() {
    n=$1
    run=$report/run$n
    mkdir -p "$run"
    # A copy of any existing package cache, so a download in one run cannot hide a difference in the other.
    if [ -d "$srcache" ]; then cp -a "$srcache" "$run/cache"; else mkdir -p "$run/cache"; fi
    export NEX_IMAGE_OUT=$run NEX_MKOSI_CACHE=$run/cache
    if [ "$do_base" = 1 ]; then
        echo "== run $n: base $(date -u +%T)"
        "$src/image/build.sh" base
        echo "== run $n: release $(date -u +%T)"
        "$src/image/build.sh" release
    else
        # A reflink copy where the file system has them: the layer step reads the base and writes next to it, never into it.
        cp -a --reflink=auto "$base_tree/base" "$run/base"
        for f in base-dpkg.tar sandbox; do [ -e "$base_tree/$f" ] && cp -a "$base_tree/$f" "$run/$f"; done
    fi
    for l in $layers; do
        echo "== run $n: $l $(date -u +%T)"
        "$src/image/mkosi.images/build-layer.sh" "$l"
    done
    echo "== run $n: done $(date -u +%T)"
}

if [ "$parallel" = 1 ]; then
    build_one 1 > "$report/run1.log" 2>&1 &
    p1=$!
    build_one 2 > "$report/run2.log" 2>&1 &
    p2=$!
    failed=0
    wait "$p1" || failed=1
    wait "$p2" || failed=1
    [ "$failed" = 0 ] || { echo "a build failed: see $report/run1.log and $report/run2.log" >&2; exit 1; }
else
    for n in 1 2; do
        # a background job keeps `set -e` active inside build_one (an `|| ...` list would switch it off)
        build_one "$n" > "$report/run$n.log" 2>&1 &
        wait $! || { echo "run $n failed: see $report/run$n.log" >&2; exit 1; }
    done
fi

# Artifacts to compare: whatever each run produced, by name.
artifacts=""
if [ "$do_base" = 1 ]; then
    for a in roothash root.erofs root.verity base.vmlinuz base.initrd base release; do
        [ -e "$report/run1/$a" ] && artifacts="$artifacts $a"
    done
fi
for l in $layers; do artifacts="$artifacts $l.raw"; done

# Digests and difference lists are computed in the container, as root: root-only files in a tree are
# otherwise silently skipped. A file's digest is its plain sha256. A tree manifest has one line per entry (path,
# mode, owner, type, size, mtime, link target) and one sha256 line per regular file, so a tree's digest (the sha256
# of the manifest) covers metadata and bytes; the listing is the diff of the two manifests (it never follows
# symlinks, so a dangling one is not an error).
printf 'artifact\trun1\trun2\tequal\n' > "$report/summary.tsv"
status=0
# shellcheck disable=SC2016
fn='manifest() ( cd "$1" || exit 1; find . -print0 | LC_ALL=C sort -z | xargs -0 stat -c "%n|%a|%u:%g|%F|%s|%Y|%N"; find . -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum ); digest() { if [ -d "$1" ]; then manifest "$1" > /tmp/manifest || return 1; [ -s /tmp/manifest ] || return 1; h=$(sha256sum < /tmp/manifest) || return 1; else h=$(sha256sum "$1") || return 1; fi; echo "${h%% *}"; }; '
# A digest is 64 hex digits or the artifact is unusable: a missing artifact, a failed container or an empty manifest
# must not turn into an equal pair of empty (or constant empty-input) digests.
digest_of() {
    # shellcheck disable=SC2086
    d=$($docker run --rm $caps -v "$report":/r "$tools" sh -c "$fn"'digest "/r/run'"$1"'/'"$2"'"') || d=""
    printf '%s' "$d"
}
is_digest() { printf '%s' "$1" | grep -Eq '^[0-9a-f]{64}$'; }
for a in $artifacts; do
    h1=$(digest_of 1 "$a")
    h2=$(digest_of 2 "$a")
    if ! is_digest "$h1" || ! is_digest "$h2"; then
        echo "cannot digest $a (run 1: '$h1', run 2: '$h2'): the artifact is missing or the digest failed" >&2
        printf '%s\t%s\t%s\t%s\n' "$a" "$h1" "$h2" error >> "$report/summary.tsv"
        status=1
        continue
    fi
    if [ "$h1" = "$h2" ]; then eq=yes; else eq=no; status=1; fi
    printf '%s\t%s\t%s\t%s\n' "$a" "$h1" "$h2" "$eq" >> "$report/summary.tsv"
    if [ "$eq" = no ]; then
        echo "difference in $a: listing and running diffoscope" >&2
        # shellcheck disable=SC2086
        $docker run --rm $caps -v "$report":/r -e A="$a" "$tools" sh -c "$fn"'
            mkdir -p /r/diff/files
            if [ -d "/r/run1/$A" ]; then
                manifest "/r/run1/$A" > /tmp/m1
                manifest "/r/run2/$A" > /tmp/m2
                diff /tmp/m1 /tmp/m2 > "/r/diff/$A.files.txt" || true
                # one diffoscope per regular file whose bytes differ or that only one run has (bounded), so a large
                # tree does not hit one global timeout; a metadata-only difference is in the listing alone
                sed -n "s/^[<>] [0-9a-f]\{64\}  \.\///p" "/r/diff/$A.files.txt" | sort -u | head -60 | {
                    i=0
                    while IFS= read -r p; do
                        i=$((i + 1))
                        printf "%s\t%s\n" "$i" "$p" >> "/r/diff/$A.index.tsv"
                        [ -e "/r/run1/$A/$p" ] && [ -e "/r/run2/$A/$p" ] || continue
                        timeout 300 diffoscope --text "/r/diff/files/$A.$i.txt" --max-report-size 2000000 "/r/run1/$A/$p" "/r/run2/$A/$p" >/dev/null 2>&1 || true
                    done
                }
            else
                timeout 600 diffoscope --text "/r/diff/$A.txt" --max-report-size 20000000 "/r/run1/$A" "/r/run2/$A" >/dev/null 2>&1 || true
            fi'
    fi
done
cat "$report/summary.tsv"
if [ "$status" = 0 ]; then
    echo "reproducible: every artifact is byte-identical"
else
    echo "NOT reproducible: see $report/diff/"
fi
exit "$status"
