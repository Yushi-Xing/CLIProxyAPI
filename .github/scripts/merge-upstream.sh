#!/usr/bin/env bash
set -euo pipefail

# Prepare a merge without committing or pushing. The caller must run regression
# checks before committing this merge and updating the fork's default branch.
upstream_url="${CPA_UPSTREAM_URL:-https://github.com/router-for-me/CLIProxyAPI.git}"

if [[ -n "$(git status --porcelain)" ]]; then
  printf '::error::Upstream synchronization requires a clean working tree.\n' >&2
  exit 1
fi

git fetch --no-tags "$upstream_url" main
upstream_revision="$(git rev-parse FETCH_HEAD)"

if git merge-base --is-ancestor "$upstream_revision" HEAD; then
  printf 'Upstream main is already included in this branch.\n'
  if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
    printf 'changed=false\n' >> "$GITHUB_OUTPUT"
  fi
  exit 0
fi

if ! git merge --no-ff --no-commit "$upstream_revision"; then
  printf '::error::Upstream merge failed; the fork will not be updated or released.\n' >&2
  git diff --name-only --diff-filter=U
  if git rev-parse --verify -q MERGE_HEAD >/dev/null; then
    git merge --abort
  fi
  exit 1
fi

if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
  printf 'changed=true\nupstream_revision=%s\n' "$upstream_revision" >> "$GITHUB_OUTPUT"
fi
printf 'Merge prepared. Run regression tests and build before committing.\n'
