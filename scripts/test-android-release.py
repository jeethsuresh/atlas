import base64
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
sys.dont_write_bytecode = True
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("android_release", Path(__file__).with_name("android-release.py"))
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


class ReleaseChecks(unittest.TestCase):
    def setUp(self):
        self.config = {"kind": "flutter", "version_file": "pubspec.yaml", "package": "example.app",
                       "certificate_sha256": "a" * 64, "min_sdk": 24}

    def test_version_sources_and_invalid_versions(self):
        self.assertEqual(release.version("version: 1.2.3+9\n", "flutter"), ("1.2.3", 9))
        self.assertEqual(release.version('versionName = "1.2.3"\nversionCode = 9', "gradle"), ("1.2.3", 9))
        for value in ("1.2.3-beta+9", "01.2.3+9", "1.2.3+0", "1.2+9"):
            with self.assertRaises(ValueError):
                release.version("version: " + value + "\n", "flutter")

    def test_changelog_matches_the_release_and_contains_real_changes(self):
        item = {"schemaVersion": 1, "version": "1.2.3", "versionCode": 9,
                "changes": ["Calendar updates", "Sign in with 🔥"]}
        self.assertEqual(release.changelog(json.dumps(item).encode(), "1.2.3", 9), item)
        for changes in ({"version": "1.2.2"}, {"versionCode": 8}, {"versionCode": True},
                        {"schemaVersion": 2}, {"schemaVersion": True}, {"extra": "unknown"},
                        {"changes": []}, {"changes": [""]}, {"changes": ["   "]},
                        {"changes": [5]}, {"changes": ["a\nb"]},
                        {"changes": ["x" * 2001]}, {"changes": ["x"] * 51}):
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                release.changelog(json.dumps({**item, **changes}).encode(), "1.2.3", 9)
        for data in (b"{", b"[]", b"x" * 32001, b'"\xff"'):
            with self.subTest(data=data[:10]), self.assertRaises((ValueError, UnicodeError)):
                release.changelog(data, "1.2.3", 9)

    def test_missing_changelog_prevents_release(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(release, "ROOT", Path(directory)):
            with self.assertRaises(FileNotFoundError):
                release.source_changelog("1.2.3", 9)

    def test_staged_changelog_is_bound_to_the_apk_manifest_and_checksums(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "app.apk").write_bytes(b"verified APK fixture")
            changes = json.dumps({"schemaVersion": 1, "version": "1.2.3", "versionCode": 9,
                                  "changes": ["Calendar updates"]}).encode()
            (root / "changelog.json").write_bytes(changes)
            certificate = "Signer #1 certificate SHA-256 digest: " + "a" * 64 + "\nVerified using v2 scheme (APK Signature Scheme v2): true"
            metadata = "package: name='example.app' versionCode='9' versionName='1.2.3'\nminSdkVersion:'24'"
            with patch.object(release, "ROOT", root), \
                 patch.dict(os.environ, {"RUNNER_TEMP": directory, "ANDROID_HOME": directory, "GITHUB_SHA": "source"}), \
                 patch.object(release.subprocess, "check_output", side_effect=[certificate, metadata]):
                release.stage({**self.config, "name": "Example", "apk": "app.apk", "asset": "example-{version}.apk"}, "1.2.3", 9)
            staged = root / "android-release-dist"
            manifest = json.loads((staged / "release-manifest.json").read_text())
            digest = hashlib.sha256(changes).hexdigest()
            self.assertEqual((staged / "changelog.json").read_bytes(), changes)
            self.assertEqual(manifest["changelog"], "changelog.json")
            self.assertEqual(manifest["changelogSha256"], digest)
            self.assertIn(digest + "  changelog.json\n", (staged / "SHA256SUMS").read_text())

    def test_changed_changelog_never_creates_a_release(self):
        with tempfile.TemporaryDirectory() as directory:
            dest = Path(directory) / "android-release-dist"
            dest.mkdir()
            (dest / "app.apk").write_bytes(b"fixture")
            (dest / "changelog.json").write_text(json.dumps({"schemaVersion": 1, "version": "1.2.3", "versionCode": 9, "changes": ["Changed after verification"]}))
            (dest / "release-manifest.json").write_text(json.dumps({"asset": "app.apk", "sha256": hashlib.sha256(b"fixture").hexdigest(), "versionCode": 9, "changelogSha256": "a" * 64}))
            with patch.dict(os.environ, {"RUNNER_TEMP": directory}), \
                 patch.object(release, "api", return_value=None), patch.object(release.subprocess, "run") as run:
                with self.assertRaises(ValueError):
                    release.publish({"name": "Example"}, "1.2.3")
                run.assert_not_called()

    def test_published_versions_are_skipped_without_overwriting(self):
        def request(path, **kwargs):
            self.assertEqual(path, "releases/tags/v1.2.3")
            return {"draft": False}
        self.assertFalse(release.publication_plan(self.config, "1.2.3", 9, request))

    def test_drafts_and_orphan_tags_fail_closed(self):
        for responses in ([{"draft": True}], [None, {"ref": "refs/tags/v1.2.3"}]):
            with self.subTest(responses=responses), self.assertRaises(ValueError):
                release.publication_plan(self.config, "1.2.3", 9, lambda *a, **k: responses.pop(0))

    def test_upgrade_requires_both_new_version_and_increasing_android_code(self):
        def request(path, **kwargs):
            if path.startswith(("releases/tags/", "git/ref/")):
                return None
            if path.startswith("releases?"):
                return [{"draft": False, "prerelease": False, "tag_name": "v1.2.3", "assets": [{"name": "app.apk"}]}]
            return {"content": base64.b64encode(b"version: 1.2.3+9\n").decode()}
        for name, code in (("1.2.2", 10), ("1.2.4", 9), ("1.2.4", 8)):
            with self.assertRaises(ValueError):
                release.publication_plan(self.config, name, code, request)
        self.assertTrue(release.publication_plan(self.config, "1.2.4", 10, request))

    def test_apk_identity_signature_and_debug_state(self):
        cert = "Signer #1 certificate SHA-256 digest: " + "a" * 64 + "\nVerified using v2 scheme (APK Signature Scheme v2): true"
        metadata = "package: name='example.app' versionCode='9' versionName='1.2.3'\nminSdkVersion:'24'"
        release.apk_identity(self.config, "1.2.3", 9, cert, metadata)
        for bad_cert, bad_metadata in ((cert.replace("a" * 64, "b" * 64), metadata),
                                       (cert.replace(": true", ": false"), metadata),
                                       (cert, metadata.replace("example.app", "wrong.app")),
                                       (cert, metadata.replace("versionCode='9'", "versionCode='8'")),
                                       (cert, metadata + "\napplication-debuggable"),
                                       (cert, metadata.replace("Version:'24'", "Version:'23'"))):
            with self.assertRaises(ValueError):
                release.apk_identity(self.config, "1.2.3", 9, bad_cert, bad_metadata)

    def test_signing_material_is_removed_when_build_fails(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            config = {**self.config, "properties_file": "key.properties", "key_alias": "permanent",
                      "build": ["build-command"], "project_dir": "."}
            env = {"RUNNER_TEMP": directory, "RELEASE_KEYSTORE_BASE64": base64.b64encode(b"fixture").decode(),
                   "RELEASE_STORE_PASSWORD": "fixture", "RELEASE_KEY_PASSWORD": "fixture", "RELEASE_KEY_ALIAS": "permanent"}
            with patch.object(release, "ROOT", root), patch.dict(os.environ, env), \
                 patch.object(release.subprocess, "check_output", return_value="SHA256: " + "AA:" * 31 + "AA"), \
                 patch.object(release.subprocess, "run", side_effect=subprocess.CalledProcessError(1, "build-command")):
                with self.assertRaises(subprocess.CalledProcessError):
                    release.build(config)
            self.assertFalse((root / "android-permanent-release.jks").exists())
            self.assertFalse((root / "key.properties").exists())

    def test_incomplete_uploaded_assets_never_publish(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "android-release-dist"
            root.mkdir()
            apk = root / "app.apk"
            apk.write_bytes(b"fixture")
            import hashlib
            changes = json.dumps({"schemaVersion": 1, "version": "1.2.3", "versionCode": 9, "changes": ["Calendar updates"]}).encode()
            (root / "changelog.json").write_bytes(changes)
            (root / "release-manifest.json").write_text(json.dumps({"asset": apk.name, "sha256": hashlib.sha256(apk.read_bytes()).hexdigest(), "versionCode": 9, "changelogSha256": hashlib.sha256(changes).hexdigest()}))
            (root / "SHA256SUMS").write_text("fixture")
            with patch.dict(os.environ, {"RUNNER_TEMP": directory, "GITHUB_REPOSITORY": "owner/app", "GITHUB_SHA": "commit"}), \
                 patch.object(release, "api", side_effect=[None, None, [{"draft": True, "tag_name": "v1.2.3", "assets": []}]]), \
                 patch.object(release.subprocess, "run") as run:
                with self.assertRaises(ValueError):
                    release.publish({"name": "Example"}, "1.2.3")
                self.assertEqual(run.call_count, 1)
                self.assertIn("--draft", run.call_args.args[0])


if __name__ == "__main__":
    unittest.main()
