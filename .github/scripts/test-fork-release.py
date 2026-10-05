#!/usr/bin/env python3
"""Regression tests for manually merged heads, release retries and date tags."""

import datetime
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location(
    "fork_release", Path(__file__).with_name("fork-release.py")
)
helper = importlib.util.module_from_spec(spec)
spec.loader.exec_module(helper)


class ReleaseTests(unittest.TestCase):
    revision = "a" * 40
    other = "b" * 40
    now = datetime.datetime(2026, 10, 3, 16, 30, tzinfo=datetime.timezone.utc)

    def release(self, tag="v6-10-04", draft=False, assets=None):
        return {
            "tag_name": tag, "draft": draft,
            "assets": [{"name": name} for name in (
                assets if assets is not None else [
                    f"CLIProxyAPI_{tag}_linux_amd64.tar.gz",
                    f"CLIProxyAPI_{tag}_linux_amd64_no-plugin.tar.gz", "checksums.txt"
                ]
            )],
        }

    def test_manually_merged_head_without_release_needs_build(self):
        self.assertFalse(helper.already_released([], {}, self.revision))

    def test_previous_commit_release_does_not_cover_current_head(self):
        self.assertFalse(helper.already_released(
            [self.release()], {"v6-10-04": self.other}, self.revision
        ))

    def test_date_uses_beijing_time(self):
        self.assertEqual(helper.release_tag([], {}, self.revision, self.now), "v6-10-04")

    def test_new_commit_same_day_gets_suffix_without_overwriting(self):
        tags = {"v6-10-04": self.other, "v6-10-04-2": self.other}
        self.assertEqual(helper.release_tag([], tags, self.revision, self.now), "v6-10-04-3")

    def test_draft_name_also_reserves_tag(self):
        self.assertEqual(helper.release_tag(
            [self.release(draft=True)], {}, self.revision, self.now
        ), "v6-10-04-2")

    def test_retry_after_midnight_reuses_same_tag(self):
        releases = [self.release(tag="v6-10-03", draft=True)]
        tags = {"v6-10-03": self.revision}
        self.assertEqual(helper.release_tag(releases, tags, self.revision, self.now), "v6-10-03")
        self.assertFalse(helper.already_released(releases, tags, self.revision))

    def test_tag_without_release_is_reused_for_retry(self):
        self.assertEqual(helper.release_tag(
            [], {"v6-10-03": self.revision}, self.revision, self.now
        ), "v6-10-03")

    def test_complete_published_date_release_skips_build(self):
        self.assertTrue(helper.already_released(
            [self.release()], {"v6-10-04": self.revision}, self.revision
        ))

    def test_legacy_release_also_skips_build(self):
        tag = "fork-20260930-0f6eb96c9567"
        self.assertTrue(helper.already_released(
            [self.release(tag=tag)], {tag: self.revision}, self.revision
        ))

    def test_missing_archive_or_checksum_requires_retry(self):
        for assets in ([], ["checksums.txt"], ["CLIProxyAPI_v6-10-04_linux_amd64_no-plugin.tar.gz"]):
            with self.subTest(assets=assets):
                self.assertFalse(helper.already_released(
                    [self.release(assets=assets)], {"v6-10-04": self.revision}, self.revision
                ))

    def test_static_only_release_requires_plugin_build(self):
        assets = ["CLIProxyAPI_v6-10-04_linux_amd64_no-plugin.tar.gz", "checksums.txt"]
        self.assertFalse(helper.already_released(
            [self.release(assets=assets)], {"v6-10-04": self.revision}, self.revision
        ))

    def test_plugin_only_release_requires_static_build(self):
        assets = ["CLIProxyAPI_v6-10-04_linux_amd64.tar.gz", "checksums.txt"]
        self.assertFalse(helper.already_released(
            [self.release(assets=assets)], {"v6-10-04": self.revision}, self.revision
        ))

    def test_target_commitish_cannot_override_real_tag(self):
        release = {**self.release(), "target_commitish": self.revision}
        self.assertFalse(helper.already_released([release], {"v6-10-04": self.other}, self.revision))

    def test_paginated_api_results_check_exact_git_commit(self):
        pages = [[self.release(tag="v6-10-03")], [self.release()]]
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "output"
            env = {"GH_REPO": "Yushi-Xing/CLIProxyAPI", "GITHUB_OUTPUT": str(output)}
            with patch.dict(os.environ, env), patch.object(helper.sys, "argv", ["fork-release.py", "check"]), patch.object(
                helper.subprocess, "check_output", side_effect=[
                    self.revision + "\n", json.dumps(pages), "v6-10-03\nv6-10-04\n",
                    self.other + "\n", self.revision + "\n",
                ]
            ):
                helper.main()
            self.assertIn("needed=false\n", output.read_text())
            self.assertIn(f"revision={self.revision}\n", output.read_text())

    def test_api_failure_or_malformed_json_never_writes_decision(self):
        for response in (subprocess.CalledProcessError(1, ["gh", "api"]), "not JSON"):
            with self.subTest(response=response), tempfile.TemporaryDirectory() as directory:
                output = Path(directory) / "output"
                env = {"GH_REPO": "Yushi-Xing/CLIProxyAPI", "GITHUB_OUTPUT": str(output)}
                with patch.dict(os.environ, env), patch.object(helper.sys, "argv", ["fork-release.py", "check"]), patch.object(
                    helper.subprocess, "check_output", side_effect=[self.revision + "\n", response]
                ):
                    with self.assertRaises((subprocess.CalledProcessError, json.JSONDecodeError)):
                        helper.main()
                self.assertFalse(output.exists())


if __name__ == "__main__":
    unittest.main()
