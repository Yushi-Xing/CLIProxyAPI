# Maintaining this fork

This fork keeps the Antigravity Chat Completions tool-image fix on `main`.
Upstream changes are merged into that branch; the branch is never reset or
force-pushed to match upstream.

## Automatic synchronization

`Sync upstream and release` runs every Monday at 02:23 UTC (10:23 in China),
and can also be started manually from the Actions tab.

1. Fetch `router-for-me/CLIProxyAPI`'s `main` branch.
2. Prepare a merge without committing or pushing it.
3. Run Antigravity converter and executor regression tests, build the static
   Linux amd64 server, and check that it starts with `--help`.
4. Only after all checks pass, commit the merge and push it to this fork.
5. Explicitly dispatch `Fork release (Linux amd64)` for that exact commit.

A merge conflict aborts the merge and fails the workflow. A regression or
build failure prevents the push. A concurrent change to remote `main` causes
the normal non-fast-forward push to fail instead of overwriting that change.
These failures leave the fork's published branch unchanged by the sync job.
Review the failed Actions run and resolve the conflict manually; no conflict
is automatically resolved by preferring one side.

No upstream changes means no new build or release. A release-job failure
does not revert an already validated merge; rerun the release workflow for
the merged commit after addressing the failure.

## Releases

`Fork release (Linux amd64)` accepts a full commit SHA that must belong to
this fork's `main` history. It reruns regression tests, compiles the static
binary, checks its linkage and startup, and then publishes a release named
`fork-YYYYMMDD-<commit>`. The asset is
`CLIProxyAPI_<tag>_linux_amd64_no-plugin.tar.gz`, accompanied by `checksums.txt`.
This build does not support dynamic library plugins.

The workflow uses `GITHUB_TOKEN` and does not require a personal access token
secret. Synchronization explicitly dispatches the release workflow because
a push made using `GITHUB_TOKEN` does not trigger an ordinary push workflow.
Release tags created using that token do not trigger upstream tag workflows.
The custom workflows run only in `Yushi-Xing/CLIProxyAPI`.

Releases contain the exact checked-out source and its embedded model catalog;
the build does not silently refresh the catalog from a different repository.
Server replacement remains manual: download the fork's release, back up and
replace `~/cliproxyapi/cli-proxy-api`, then restart the service. The original
installer downloads upstream releases and can overwrite the fork's fix.

## Manual conflict resolution

In a clean local checkout:

```bash
git switch main
git pull --ff-only origin main
git fetch https://github.com/router-for-me/CLIProxyAPI.git main
git merge --no-ff --no-commit FETCH_HEAD
# Resolve conflicts, preserving the tool-image regression tests.
go test ./internal/translator/antigravity/... ./internal/translator/common -count=1
go test ./internal/runtime/executor -run Antigravity -count=1
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /tmp/cli-proxy-api ./cmd/server
git commit
git push origin main
gh workflow run fork-release.yml --ref main -f "revision=$(git rev-parse HEAD)"
```

If upstream later fixes the same image conversion, review the implementations
and remove the redundant fork patch while retaining regression coverage.
