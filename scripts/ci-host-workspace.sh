#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Print the path the Docker daemon sees for the current workspace, for NEX_HOST_WORKSPACE
# (scripts/ci-docker.sh and the tests/integration/image_*.py labs). On a runner whose workspace is a
# host path that is $PWD; when the job runs inside a container the workspace is a Docker volume, so the
# volume's mount point is read from the daemon. Exits non-zero, saying why, when neither holds.
#
#   scripts/ci-host-workspace.sh [FILE]    FILE is a path in the repository used to prove the mount (default image/build.sh)
set -euo pipefail
probe_file=${1:-image/build.sh}
docker info > /dev/null
probe() {
    docker run --rm -v "$1:/probe:ro" --entrypoint sh debian:trixie-slim -c "test -f /probe/$probe_file" 2> /dev/null
}
host=$PWD
if ! probe "$host"; then
    # Jobs run inside a container: the workspace is a named volume. Read which one from our own
    # mount table and ask the daemon where it lives on the host.
    root=$(awk -v w="$PWD" '$5 == w { print $4 }' /proc/self/mountinfo)
    name=$(printf '%s\n' "$root" | sed -n 's#.*/volumes/\([^/]*\)/_data.*#\1#p')
    [ -n "$name" ] || {
        echo "workspace $PWD is neither a Docker host path nor a Docker volume (mount root: '$root')" >&2
        exit 1
    }
    host=$(docker volume inspect -f '{{.Mountpoint}}' "$name")
    probe "$host" || {
        echo "Docker host path $host for volume $name does not contain this workspace" >&2
        exit 1
    }
fi
printf '%s\n' "$host"
