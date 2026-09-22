#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Docker wrapper for CI runners that execute jobs inside a container. There the job
# workspace is a Docker volume, so a bind mount of "$PWD/..." names a path the daemon
# cannot see and mounts an empty directory. This rewrites every -v source under the
# workspace to the same directory's path on the Docker host (NEX_HOST_WORKSPACE) and
# refuses any other host path, so a build fails loudly instead of building from nothing.
# On a runner whose workspace is already a host path, set NEX_HOST_WORKSPACE="$PWD".
#
# Every container also gets the nex-build-cache volume, which lives on the Docker host and
# outlives the job workspace: the Go build cache, the npm cache and the ISO's pinned Debian
# downloads are reused by the next run instead of fetched again.
set -eu
workspace=${GITHUB_WORKSPACE:-$PWD}
host=${NEX_HOST_WORKSPACE:?set NEX_HOST_WORKSPACE to the workspace path on the Docker host}
args=()
while (($#)); do
    case $1 in
        run)
            if ((${#args[@]} == 0)); then
                args+=(run -v nex-build-cache:/cache -e GOCACHE=/cache/go-build
                       -e npm_config_cache=/cache/npm -e NEX_DOWNLOADS=/cache/downloads)
            else
                args+=("$1")
            fi ;;
        -v|--volume)
            spec=$2
            shift
            if [[ $spec == "$workspace" || $spec == "$workspace"/* || $spec == "$workspace":* ]]; then
                spec=$host${spec#"$workspace"}
            elif [[ $spec == /* ]]; then
                echo "ci-docker: refusing to mount $spec: only paths under $workspace exist on the Docker host" >&2
                exit 1
            fi
            args+=(-v "$spec") ;;
        *) args+=("$1") ;;
    esac
    shift
done
exec docker "${args[@]}"
