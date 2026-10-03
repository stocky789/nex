#!/bin/bash
# SPDX-License-Identifier: AGPL-3.0-only
# Stream only the commit-tagged demo image to the fixed root deployment wrapper.
set -euo pipefail

for name in NEX_DEPLOY_HOST NEX_DEPLOY_USER NEX_DEPLOY_KEY NEX_DEPLOY_KNOWN_HOSTS; do
  if [[ -z ${!name:-} ]]; then
    echo "demo-ci-deploy: required secret $name is missing; refusing to skip deployment" >&2
    exit 1
  fi
done
release=${GITHUB_SHA:-${GITEA_SHA:-}}
if [[ ! $release =~ ^[0-9a-f]{40}$ ]]; then
  echo 'demo-ci-deploy: expected a full lowercase commit SHA' >&2
  exit 1
fi
if [[ ! $NEX_DEPLOY_USER =~ ^[a-z_][a-z0-9_-]*$ || ! $NEX_DEPLOY_HOST =~ ^[A-Za-z0-9][A-Za-z0-9.-]*$ ]]; then
  echo 'demo-ci-deploy: invalid deployment user or host' >&2
  exit 1
fi
image="nex-demo:$release"
if [[ $(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$image") != "$release" ]]; then
  echo 'demo-ci-deploy: image revision does not match this release' >&2
  exit 1
fi

umask 077
keydir=$(mktemp -d)
trap 'rm -rf "$keydir"' EXIT
printf '%s\n' "$NEX_DEPLOY_KEY" >"$keydir/id"
printf '%s\n' "$NEX_DEPLOY_KNOWN_HOSTS" >"$keydir/known_hosts"
ssh_opts=(-T -i "$keydir/id" -o BatchMode=yes -o IdentitiesOnly=yes
          -o UserKnownHostsFile="$keydir/known_hosts" -o StrictHostKeyChecking=yes
          -o ConnectTimeout=15 -o ServerAliveInterval=15 -o ServerAliveCountMax=4
          -o ForwardAgent=no -o ClearAllForwardings=yes)
docker image save "$image" | gzip -1 | ssh "${ssh_opts[@]}" "$NEX_DEPLOY_USER@$NEX_DEPLOY_HOST" \
  "sudo -n /usr/local/sbin/nex-demo-deploy $release"
echo "demo-ci-deploy: healthchecked release $release deployed"
