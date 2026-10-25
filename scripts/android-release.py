#!/usr/bin/env python3
"""Build and publish immutable Android releases using GitHub environment secrets."""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import urllib.error
import urllib.parse
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
SEMVER = re.compile(r"(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)")


def version(source, kind):
    if kind == "flutter":
        match = re.search(r"^version:\s*(\d+\.\d+\.\d+)\+(\d+)\s*$", source, re.M)
        if not match:
            raise ValueError("Use a stable version and positive build number in pubspec.yaml")
        name, code = match.groups()
    else:
        name = re.search(r'versionName\s*=\s*"([^"]+)"', source)
        code = re.search(r"versionCode\s*=\s*(\d+)", source)
        if not name or not code:
            raise ValueError("Set versionName and versionCode in the Android build file")
        name, code = name[1], code[1]
    if not SEMVER.fullmatch(name) or int(code) < 1:
        raise ValueError("Use a stable semantic version and positive version code")
    return name, int(code)


def semver(name):
    match = SEMVER.fullmatch(name)
    if not match:
        raise ValueError("Invalid stable version")
    return tuple(map(int, match.groups()))


def changelog(data, name, code):
    if len(data) > 32000:
        raise ValueError("changelog.json must be at most 32000 UTF-8 bytes")
    item = json.loads(data.decode("utf-8"))
    if (not isinstance(item, dict) or set(item) != {"schemaVersion", "version", "versionCode", "changes"}
            or type(item["schemaVersion"]) is not int or item["schemaVersion"] != 1
            or item["version"] != name or type(item["versionCode"]) is not int or item["versionCode"] != code):
        raise ValueError("changelog.json must use schemaVersion 1 and match the source version and Android version code")
    changes = item["changes"]
    if (not isinstance(changes, list) or not 1 <= len(changes) <= 50
            or any(not isinstance(text, str) or not text.strip() or len(text.encode("utf-8")) > 2000
                   or any(ord(char) < 32 for char in text) for text in changes)):
        raise ValueError("changelog.json requires 1-50 nonempty plain-text changes, at most 2000 UTF-8 bytes each")
    return item


def source_changelog(name, code):
    return changelog((ROOT / "changelog.json").read_bytes(), name, code)


def api(path, missing=False):
    repo = os.environ["GITHUB_REPOSITORY"]
    url = f"https://api.github.com/repos/{repo}/{path}"
    request = urllib.request.Request(url, headers={
        "Authorization": "Bearer " + os.environ["GH_TOKEN"],
        "Accept": "application/vnd.github+json",
        "X-GitHub-Api-Version": "2022-11-28",
    })
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            return json.load(response)
    except urllib.error.HTTPError as error:
        if missing and error.code == 404:
            return None
        raise RuntimeError(f"GitHub request failed with HTTP {error.code}: {path}") from None


def publication_plan(config, name, code, request=api):
    tag = "v" + name
    release = request("releases/tags/" + tag, missing=True)
    if release:
        if release["draft"]:
            raise ValueError("An existing draft needs review; releases are never overwritten")
        return False
    if request("git/ref/tags/" + tag, missing=True):
        raise ValueError("This tag already exists; increase the version")
    published = []
    for page in range(1, 11):
        releases = request(f"releases?per_page=100&page={page}")
        for item in releases:
            if item["tag_name"] == tag:
                raise ValueError("An existing draft needs review; releases are never overwritten")
            candidate = item["tag_name"].removeprefix("v")
            if (not item["draft"] and not item["prerelease"] and SEMVER.fullmatch(candidate)
                    and any(asset["name"].endswith(".apk") for asset in item["assets"])):
                published.append(item)
        if len(releases) < 100:
            break
    else:
        raise ValueError("Release history is too large to validate automatically")
    if published:
        previous = max(published, key=lambda item: semver(item["tag_name"].removeprefix("v")))
        if semver(name) <= semver(previous["tag_name"].removeprefix("v")):
            raise ValueError("The new version must exceed the latest published Android release")
        path = urllib.parse.quote(config["version_file"], safe="/")
        old = request("contents/" + path + "?ref=" + urllib.parse.quote(previous["tag_name"], safe=""))
        old_name, old_code = version(base64.b64decode(old["content"]).decode(), config["kind"])
        if old_name != previous["tag_name"].removeprefix("v") or code <= old_code:
            raise ValueError("Increase the version code; existing Android installations must update in place")
    return True


def properties(value):
    return value.replace("\\", "\\\\").replace("\n", "\\n").replace("\r", "\\r").replace(" ", "\\ ")


def build(config):
    values = {key: os.environ.get(key, "") for key in (
        "RELEASE_KEYSTORE_BASE64", "RELEASE_STORE_PASSWORD", "RELEASE_KEY_ALIAS", "RELEASE_KEY_PASSWORD")}
    if not all(values.values()):
        raise ValueError("Configure all four RELEASE_* secrets in the main-only android-release environment")
    if values["RELEASE_KEY_ALIAS"] != config["key_alias"]:
        raise ValueError("The signing alias does not match the permanent app identity")
    key = Path(os.environ["RUNNER_TEMP"]) / "android-permanent-release.jks"
    props = ROOT / config["properties_file"] if config.get("properties_file") else None
    if props and props.exists():
        raise ValueError("Refusing to overwrite an existing signing properties file")
    env = dict(os.environ)
    try:
        key.write_bytes(base64.b64decode(values["RELEASE_KEYSTORE_BASE64"], validate=True))
        key.chmod(0o600)
        info = subprocess.check_output([
            "keytool", "-list", "-v", "-keystore", str(key),
            "-storepass:env", "RELEASE_STORE_PASSWORD", "-alias", config["key_alias"]], env=env, text=True)
        certificate = re.search(r"SHA256: ([A-Fa-f0-9:]+)", info)
        if not certificate or certificate[1].replace(":", "").lower() != config["certificate_sha256"]:
            raise ValueError("The keystore does not match the pinned permanent certificate")
        for prefix in ("RELEASE", "STORE", "RIVER", "SPACETIME"):
            env[prefix + "_KEYSTORE"] = str(key)
            env[prefix + "_STORE_PASSWORD"] = values["RELEASE_STORE_PASSWORD"]
            env[prefix + "_KEY_ALIAS"] = values["RELEASE_KEY_ALIAS"]
            env[prefix + "_KEY_PASSWORD"] = values["RELEASE_KEY_PASSWORD"]
        if props:
            props.write_text("\n".join(k + "=" + properties(v) for k, v in {
                "storeFile": str(key), "storePassword": values["RELEASE_STORE_PASSWORD"],
                "keyAlias": values["RELEASE_KEY_ALIAS"], "keyPassword": values["RELEASE_KEY_PASSWORD"],
            }.items()) + "\n")
            props.chmod(0o600)
        subprocess.run(config["build"], cwd=ROOT / config["project_dir"], env=env, check=True)
    finally:
        key.unlink(missing_ok=True)
        if props:
            props.unlink(missing_ok=True)


def apk_identity(config, name, code, certificates, metadata):
    actual = re.findall(r"^Signer #\d+ certificate SHA-256 digest: ([a-f0-9]+)$", certificates, re.M)
    if actual != [config["certificate_sha256"]]:
        raise ValueError("APK signature does not match the permanent certificate")
    if not any(f"Verified using {scheme} scheme (APK Signature Scheme {scheme}): true" in certificates for scheme in ("v2", "v3")):
        raise ValueError("APK must have a verified v2 or v3 signature")
    match = re.search(r"package: name='([^']+)' versionCode='(\d+)' versionName='([^']+)'", metadata)
    if not match or match.groups() != (config["package"], str(code), name):
        raise ValueError("APK package or version does not match the checked-out source")
    minimum = re.search(r"(?:minSdkVersion|sdkVersion):'(\d+)'", metadata)
    if not minimum or int(minimum[1]) != config["min_sdk"] or "application-debuggable" in metadata:
        raise ValueError("APK minimum Android version changed or the APK is debuggable")


def stage(config, name, code):
    source_changelog(name, code)
    apk = ROOT / config["apk"]
    tools = Path(os.environ["ANDROID_HOME"]) / "build-tools/35.0.0"
    certificates = subprocess.check_output([str(tools / "apksigner"), "verify", "--verbose", "--print-certs", str(apk)], text=True)
    metadata = subprocess.check_output([str(tools / "aapt2"), "dump", "badging", str(apk)], text=True)
    apk_identity(config, name, code, certificates, metadata)
    for command in config.get("additional_verification", []):
        subprocess.run(command, cwd=ROOT, check=True)
    dest = Path(os.environ["RUNNER_TEMP"]) / "android-release-dist"
    dest.mkdir(exist_ok=True)
    asset = dest / config["asset"].format(version=name, tag="v" + name)
    shutil.copyfile(apk, asset)
    checksum = hashlib.sha256(asset.read_bytes()).hexdigest()
    changes = dest / "changelog.json"
    shutil.copyfile(ROOT / "changelog.json", changes)
    changes_digest = hashlib.sha256(changes.read_bytes()).hexdigest()
    (dest / "SHA256SUMS").write_text(checksum + "  " + asset.name + "\n" + changes_digest + "  changelog.json\n")
    manifest = {"package": config["package"], "version": name, "versionCode": code,
                "certificateSha256": config["certificate_sha256"], "sha256": checksum,
                "asset": asset.name, "size": asset.stat().st_size, "commit": os.environ["GITHUB_SHA"],
                "changelog": "changelog.json", "changelogSha256": changes_digest}
    (dest / "release-manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    print("Verified", config["name"], name, "build", code, "SHA256", checksum)


def publish(config, name):
    tag = "v" + name
    if api("releases/tags/" + tag, missing=True) or api("git/ref/tags/" + tag, missing=True):
        raise ValueError("A release or tag appeared during the build; refusing to overwrite it")
    dest = Path(os.environ["RUNNER_TEMP"]) / "android-release-dist"
    files = sorted(dest.iterdir())
    manifest = json.loads((dest / "release-manifest.json").read_text())
    if hashlib.sha256((dest / manifest["asset"]).read_bytes()).hexdigest() != manifest["sha256"]:
        raise ValueError("The staged APK changed after verification")
    changes_data = (dest / "changelog.json").read_bytes()
    changes = changelog(changes_data, name, manifest["versionCode"])
    if manifest["changelogSha256"] != hashlib.sha256(changes_data).hexdigest():
        raise ValueError("The staged changelog changed after verification")
    repo = os.environ["GITHUB_REPOSITORY"]
    notes = "What's new in " + name + "\n\n" + "\n".join("- " + text for text in changes["changes"])
    notes += "\n\nInstall the APK over the existing app. See SHA256SUMS and release-manifest.json for the package, permanent certificate and download digest."
    subprocess.run(["gh", "release", "create", tag, "--repo", repo, "--target", os.environ["GITHUB_SHA"],
                    "--draft", "--title", config["name"] + " " + name, "--notes", notes, *map(str, files)], check=True)
    release = next((item for item in api("releases?per_page=100") if item["tag_name"] == tag), None)
    if not release or not release["draft"]:
        raise ValueError("The uploaded draft could not be verified")
    assets = {item["name"]: item for item in release["assets"]}
    if set(assets) != {p.name for p in files}:
        raise ValueError("Uploaded assets are incomplete; the draft remains unpublished")
    for path in files:
        actual = assets[path.name]
        if actual["state"] != "uploaded" or actual["size"] != path.stat().st_size or actual.get("digest") != "sha256:" + hashlib.sha256(path.read_bytes()).hexdigest():
            raise ValueError("Uploaded asset verification failed; the draft remains unpublished")
    subprocess.run(["gh", "release", "edit", tag, "--repo", repo, "--draft=false", "--latest"], check=True)
    print("Published verified Android release:", repo, tag)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("action", choices=["validate", "plan", "build", "stage", "publish"])
    args = parser.parse_args()
    config = json.loads((ROOT / "android-release.json").read_text())
    name, code = version((ROOT / config["version_file"]).read_text(), config["kind"])
    source_changelog(name, code)
    if args.action == "validate":
        print("Validated source version and matching changelog.json:", name, code)
    elif args.action == "plan":
        wants_publish = os.environ.get("PUBLISH_RELEASE", "true").lower() == "true"
        run = publication_plan(config, name, code) if wants_publish else True
        with open(os.environ["GITHUB_OUTPUT"], "a") as output:
            output.write(f"build={str(run).lower()}\npublish={str(wants_publish and run).lower()}\nversion={name}\n")
        print("Release", name, "build", code, "will build" if run else "already published; skipping")
    elif args.action == "build":
        build(config)
    elif args.action == "stage":
        stage(config, name, code)
    else:
        publish(config, name)


if __name__ == "__main__":
    main()
