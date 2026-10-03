#!/usr/bin/env python3
"""Identify released commits and allocate stable, collision-free date tags."""

import datetime
import json
import os
import re
import subprocess
import sys
from zoneinfo import ZoneInfo


DATE_TAG = re.compile(r"v\d-\d{2}-\d{2}(?:-(?:[2-9]|[1-9]\d+))?$")
LEGACY_TAG = re.compile(r"fork-\d{8}-[0-9a-f]{12,40}$")


def complete(release):
    tag = release["tag_name"]
    required = {f"CLIProxyAPI_{tag}_linux_amd64_no-plugin.tar.gz", "checksums.txt"}
    assets = {asset["name"] for asset in release.get("assets", [])}
    return not release["draft"] and required <= assets


def already_released(releases, tags, revision):
    return any(
        (DATE_TAG.fullmatch(r["tag_name"]) or LEGACY_TAG.fullmatch(r["tag_name"]))
        and tags.get(r["tag_name"]) == revision and complete(r)
        for r in releases
    )


def release_tag(releases, tags, revision, now):
    # Reuse a date tag on retries, including retries after midnight. An
    # existing release's target_commitish can be a branch, so verify Git tags.
    candidates = [
        r for r in releases
        if DATE_TAG.fullmatch(r["tag_name"]) and tags.get(r["tag_name"]) == revision
    ]
    candidates.sort(key=lambda r: (not complete(r), r["tag_name"]))
    if candidates:
        return candidates[0]["tag_name"]
    for tag in sorted(tags):
        if DATE_TAG.fullmatch(tag) and tags[tag] == revision:
            return tag
    local_date = now.astimezone(ZoneInfo("Asia/Shanghai"))
    base = f"v{local_date.year % 10}-{local_date:%m-%d}"
    occupied = set(tags) | {r["tag_name"] for r in releases}
    tag = base
    sequence = 2
    while tag in occupied:
        tag = f"{base}-{sequence}"
        sequence += 1
    return tag


def main():
    if len(sys.argv) != 2 or sys.argv[1] not in {"check", "plan"}:
        raise SystemExit("usage: fork-release.py check|plan")
    revision = subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip()
    # Fail closed on authentication/network/JSON errors; never allocate a tag
    # or dispatch a build using incomplete release data.
    repository = os.environ["GH_REPO"]
    pages = json.loads(subprocess.check_output(
        ["gh", "api", "--paginate", "--slurp", f"repos/{repository}/releases?per_page=100"],
        text=True,
    ))
    releases = [release for page in pages for release in page]
    names = subprocess.check_output(["git", "tag", "--list"], text=True).splitlines()
    tags = {}
    for name in names:
        if DATE_TAG.fullmatch(name) or LEGACY_TAG.fullmatch(name):
            tags[name] = subprocess.check_output(
                ["git", "rev-list", "-n", "1", name], text=True
            ).strip()
        else:
            tags[name] = None  # All existing tag names reserve their namespace.
    needed = not already_released(releases, tags, revision)
    tag = release_tag(releases, tags, revision, datetime.datetime.now(datetime.timezone.utc))
    with open(os.environ["GITHUB_OUTPUT"], "a", encoding="utf-8") as output:
        output.write(f"revision={revision}\ntag={tag}\nneeded={str(needed).lower()}\n")
    print(f"Commit {revision}: {'release needed' if needed else 'already published'}; date tag {tag}")


if __name__ == "__main__":
    main()
