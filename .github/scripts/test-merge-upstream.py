#!/usr/bin/env python3
"""Exercise upstream merging in isolated local Git repositories."""

import os
from pathlib import Path
import subprocess
import tempfile
import unittest


MERGE_SCRIPT = Path(__file__).with_name("merge-upstream.sh").resolve()


class MergeUpstreamTest(unittest.TestCase):
    def setUp(self):
        self.workspace = tempfile.TemporaryDirectory(prefix="cpa-sync-test-")
        self.addCleanup(self.workspace.cleanup)
        self.root = Path(self.workspace.name)
        self.upstream = self.root / "upstream"
        self.fork = self.root / "fork"
        self.output = self.root / "outputs"
        self.env = {
            **os.environ,
            "GIT_CONFIG_GLOBAL": os.devnull,
            "GIT_CONFIG_SYSTEM": os.devnull,
            "GIT_AUTHOR_NAME": "Sync Test",
            "GIT_AUTHOR_EMAIL": "sync-test@example.com",
            "GIT_COMMITTER_NAME": "Sync Test",
            "GIT_COMMITTER_EMAIL": "sync-test@example.com",
            "CPA_UPSTREAM_URL": str(self.upstream),
            "GITHUB_OUTPUT": str(self.output),
        }
        self.git(self.root, "init", "--initial-branch=main", str(self.upstream))
        self.commit_file(self.upstream, "common.txt", "original\n")
        self.git(self.root, "clone", str(self.upstream), str(self.fork))
        self.commit_file(self.fork, "fork-fix.txt", "retain the image fix\n")

    def git(self, cwd, *args):
        result = subprocess.run(
            ["git", *args], cwd=cwd, env=self.env,
            text=True, capture_output=True, check=True,
        )
        return result.stdout.strip()

    def commit_file(self, repo, filename, content):
        (repo / filename).write_text(content)
        self.git(repo, "add", filename)
        self.git(repo, "commit", "-m", "update " + filename)

    def merge(self):
        return subprocess.run(
            ["bash", str(MERGE_SCRIPT)], cwd=self.fork, env=self.env,
            text=True, capture_output=True,
        )

    def test_clean_merge_is_prepared_without_committing_or_pushing(self):
        self.commit_file(self.upstream, "new-feature.txt", "upstream feature\n")
        fork_head = self.git(self.fork, "rev-parse", "HEAD")
        upstream_head = self.git(self.upstream, "rev-parse", "HEAD")
        result = self.merge()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.git(self.fork, "rev-parse", "HEAD"), fork_head)
        self.assertEqual(self.git(self.upstream, "rev-parse", "HEAD"), upstream_head)
        self.assertEqual(self.git(self.fork, "rev-parse", "MERGE_HEAD"), upstream_head)
        self.assertEqual((self.fork / "fork-fix.txt").read_text(), "retain the image fix\n")
        self.assertEqual((self.fork / "new-feature.txt").read_text(), "upstream feature\n")
        self.assertIn("changed=true\n", self.output.read_text())
        self.assertIn("upstream_revision=" + upstream_head, self.output.read_text())

    def test_up_to_date_branch_is_unchanged(self):
        fork_head = self.git(self.fork, "rev-parse", "HEAD")
        result = self.merge()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.git(self.fork, "rev-parse", "HEAD"), fork_head)
        self.assertEqual(self.git(self.fork, "status", "--porcelain"), "")
        self.assertEqual(self.output.read_text(), "changed=false\n")

    def test_conflict_is_aborted_and_fork_is_restored(self):
        self.commit_file(self.fork, "common.txt", "fork change\n")
        self.commit_file(self.upstream, "common.txt", "conflicting upstream change\n")
        fork_head = self.git(self.fork, "rev-parse", "HEAD")
        result = self.merge()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Upstream merge failed", result.stderr)
        self.assertEqual(self.git(self.fork, "rev-parse", "HEAD"), fork_head)
        self.assertEqual(self.git(self.fork, "status", "--porcelain"), "")
        self.assertFalse((self.fork / ".git" / "MERGE_HEAD").exists())
        self.assertEqual((self.fork / "common.txt").read_text(), "fork change\n")
        self.assertEqual((self.fork / "fork-fix.txt").read_text(), "retain the image fix\n")
        self.assertFalse(self.output.exists())

    def test_dirty_checkout_is_rejected(self):
        (self.fork / "local-note.txt").write_text("local work\n")
        fork_head = self.git(self.fork, "rev-parse", "HEAD")
        result = self.merge()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("clean working tree", result.stderr)
        self.assertEqual(self.git(self.fork, "rev-parse", "HEAD"), fork_head)
        self.assertEqual((self.fork / "local-note.txt").read_text(), "local work\n")
        self.assertFalse(self.output.exists())


if __name__ == "__main__":
    unittest.main()
