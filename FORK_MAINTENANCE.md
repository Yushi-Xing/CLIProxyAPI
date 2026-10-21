# Maintaining this fork

This fork keeps the Antigravity Chat Completions tool-image fix on `main`.
Upstream changes are merged into that branch; the branch is never reset or
force-pushed to match upstream.

## Manual synchronization

`Sync upstream and release` runs only when started manually from the Actions
tab. Automatic schedules and push-triggered workflows remain disabled.
The workflow performs these steps:

1. Fetch `router-for-me/CLIProxyAPI`'s `main` branch.
2. Prepare a merge without committing or pushing it.
3. Run Antigravity converter and executor regression tests, plugin host and
   management support-header tests, build both Linux amd64 variants, and
   check that they start with `--help`.
4. Only after all checks pass, commit the merge and push it to this fork.
5. Check whether the current commit has a published release with both build
   archives and the checksum asset. If it does not, explicitly dispatch
   `Fork release (Linux amd64)` for that exact commit.

A merge conflict aborts the merge and fails the workflow. A regression or
build failure prevents the push. A concurrent change to remote `main` causes
the normal non-fast-forward push to fail instead of overwriting that change.
These failures leave the fork's published branch unchanged by the sync job.
Review the failed Actions run and resolve the conflict manually; no conflict
is automatically resolved by preferring one side.

An unchanged upstream still triggers a build when the current fork commit
has not been released, including after a manual merge. An existing complete
release skips the build. Drafts or releases missing required assets are
retried; API failures fail the check instead of being treated as missing
releases. A release-job failure does not revert an already validated merge;
rerun either custom workflow after addressing the failure.

## Releases

`Fork release (Linux amd64)` accepts a full commit SHA that must belong to
this fork's `main` history. It reruns regression tests, compiles both Linux
builds, checks their linkage and startup, and then publishes a release named
`vY-MM-DD`, using the last digit of the year and the publication date in
Asia/Shanghai (for example, `v6-10-04` on October 4, 2026). Further commits
published on the same day use `v6-10-04-2`, `v6-10-04-3`, and so on. Retries
reuse the existing tag for the same commit, even after midnight; existing
tags are never moved to a different commit. Release checks resolve Git tags
to commit SHAs and also recognize complete releases with the older
`fork-YYYYMMDD-<commit>` naming scheme. The assets are the following two
archives, accompanied by `checksums.txt`:

- `CLIProxyAPI_<tag>_linux_amd64.tar.gz`: standard build with dynamic library
  plugin support. It is built with CGO in the upstream manylinux2014 image
  and checked against a GLIBC 2.17 baseline. Use this for the plugin marketplace.
- `CLIProxyAPI_<tag>_linux_amd64_no-plugin.tar.gz`: portable static build for
  systems without compatible GLIBC. It does not support dynamic library plugins.

The plugin-enabled build runs plugin host and management support-header tests.
A release missing either build is incomplete and will be retried by synchronization.

To release a manually merged commit directly, select `Fork release (Linux
amd64)` in Actions, click `Run workflow`, select `main`, and enter the full
40-character commit SHA in `revision`. The upstream `release` workflow is
tag-triggered and is not this fork's static build workflow.

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

## Streaming and routing compatibility

The v8.0.36 integration retains native Antigravity tool-image parts,
plugin-authorized candidate filtering, delivery-aware accounting, and the
360-second default first-output deadline. The deadline ends at meaningful
model output; it does not limit the total generation time.

`streaming.send-keepalive-before-ft` and the legacy
`streaming.keepalive-before-first-chunk` both enable the same startup heartbeat
when `streaming.keepalive-seconds` is positive. Either flag is sufficient.
Both are opt-in; existing configurations retain their behavior.

With `routing.session-affinity-rebind-on: quota`, a transient failure preserves
the original binding only while the billing scheduler still authorizes that
credential. The host may perform one additional permission-filter query for
unavailable affinity candidates. Only currently available, authorized
credentials can execute a request. Legacy account-picking schedulers are
never offered unavailable credentials. The default `any` mode remains
unchanged.
