#!/bin/bash
# SPDX-License-Identifier: AGPL-3.0-only
# Copy a built static site to the website host and publish it via nex-website-publish.
# Requires NEX_DEPLOY_HOST, NEX_DEPLOY_USER, NEX_DEPLOY_KEY, NEX_DEPLOY_KNOWN_HOSTS.
# Optional: NEX_WEBSITE_ARTIFACT (default website/build), NEX_WEBSITE_RELEASE_ID.
set -euo pipefail

ARTIFACT=${NEX_WEBSITE_ARTIFACT:-website/build}
RELEASE_ID=${NEX_WEBSITE_RELEASE_ID:-}

if [[ -z "${NEX_DEPLOY_HOST:-}" || -z "${NEX_DEPLOY_USER:-}" || -z "${NEX_DEPLOY_KEY:-}" || -z "${NEX_DEPLOY_KNOWN_HOSTS:-}" ]]; then
  echo 'website-ci-deploy: set every NEX_DEPLOY_* secret or skip this job' >&2
  exit 1
fi

if [[ ! -d $ARTIFACT ]]; then
  echo "website-ci-deploy: missing artifact directory $ARTIFACT" >&2
  exit 1
fi

if [[ -z $RELEASE_ID ]]; then
  stamp=$(date -u +%Y%m%d%H%M%S)
  sha=${GITHUB_SHA:-${GITEA_SHA:-local}}
  sha=${sha:0:12}
  RELEASE_ID="${stamp}-${sha}"
fi

if [[ ! $RELEASE_ID =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$ ]]; then
  echo "website-ci-deploy: invalid release id $RELEASE_ID" >&2
  exit 1
fi

umask 077
keydir=$(mktemp -d)
trap 'rm -rf "$keydir"' EXIT
printf '%s\n' "$NEX_DEPLOY_KEY" >"$keydir/id"
printf '%s\n' "$NEX_DEPLOY_KNOWN_HOSTS" >"$keydir/known_hosts"
chmod 600 "$keydir/id"
ssh_opts=(-i "$keydir/id" -o IdentitiesOnly=yes -o UserKnownHostsFile="$keydir/known_hosts" -o StrictHostKeyChecking=yes)
target="$NEX_DEPLOY_USER@$NEX_DEPLOY_HOST"
staging="/tmp/nex-website-$RELEASE_ID"

find "$ARTIFACT" -type f ! -type l -exec chmod a-x {} +

ssh "${ssh_opts[@]}" "$target" "rm -rf '$staging' && mkdir -m 700 '$staging'"
# Trailing slash: copy contents into staging/build/
scp -r "${ssh_opts[@]}" "$ARTIFACT" "$target:$staging/build"
ssh "${ssh_opts[@]}" "$target" "set -eu
  sudo -n /usr/local/sbin/nex-website-publish publish '$staging/build' '$RELEASE_ID'
  rm -rf '$staging'"

echo "website-ci-deploy: published $RELEASE_ID"
