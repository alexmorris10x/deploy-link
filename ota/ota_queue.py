#!/usr/bin/env python3
"""Queue signed iOS builds and serve them for over-the-air install through private Tailscale HTTPS.

`publish` turns a signed development or ad hoc `.app` (or `.ipa`) into a queued
release. `serve` runs one loopback server that `tailscale serve` exposes to the
tailnet. The Deploy Link iPhone app (and the HTML page at `/`) lists the queue
and hands each release's `itms-services` link to iOS, which shows its own
install prompt and downloads the IPA straight from this Mac. Nothing is hosted
anywhere else, and this works on Wi-Fi or cellular as long as the device has
Tailscale connected.

The app tags each install link with its device (`?device=<id>&family=iPhone`),
so the server knows which download is running where and remembers, per device,
the last build of each app that finished downloading (`devices.json`). The app
uses that to block a second download of the same app and to ask before
reinstalling a build the device already has.

Only devices listed in the app's provisioning profile can install a release.
Releases leave the queue when a newer one pushes them past the newest `keep`
per app, when their provisioning profile expires, or on `remove` and `prune`.
Remote deletion requires a separate management credential; reading and
downloading are authorized by access to your Tailscale network.
"""

from __future__ import annotations

import argparse
import hashlib
import hmac
import html
import json
import os
import plistlib
import re
import secrets
import shutil
import stat
import subprocess
import sys
import tempfile
import threading
import zipfile
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Optional
from urllib.parse import parse_qs, quote, urlencode, urlparse

SCHEMA_VERSION = 1
DEFAULT_ROOT = Path(os.environ.get("DEPLOY_LINK_QUEUE_ROOT", "~/.local/share/ios-ota/queue")).expanduser()
DEFAULT_PORT = 8477
DEFAULT_KEEP = 1
RELEASE_ID = re.compile(r"^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{8}$")
DEVICE_ID = re.compile(r"^[A-Za-z0-9-]{1,64}$")
FAMILIES = ("iPhone", "iPad")
CHUNK = 1024 * 1024
MANAGEMENT_TOKEN_NAME = ".management-token"
MANAGEMENT_TOKEN = re.compile(r"^[0-9a-f]{64}$")


def utc_now() -> str:
    return datetime.now(timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def private_directory(path: Path) -> None:
    """Create an owner-only directory without following a substituted final symlink."""
    if path.is_symlink():
        raise ValueError("the queue directory must not be a symbolic link")
    path.mkdir(mode=0o700, parents=True, exist_ok=True)
    info = path.stat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid():
        raise ValueError("the queue directory must be owned by the current user")
    path.chmod(0o700)


class ManagementCredential:
    """A local capability for deletion; never part of feed data, URLs, or logs."""

    def __init__(self, queue_root: Path):
        self.path = queue_root / MANAGEMENT_TOKEN_NAME

    def ensure(self) -> None:
        private_directory(self.path.parent)
        try:
            fd = os.open(self.path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        except FileExistsError:
            self.read()
            return
        with os.fdopen(fd, "w", encoding="ascii") as output:
            output.write(secrets.token_hex(32) + "\n")
            output.flush()
            os.fsync(output.fileno())

    def read(self) -> str:
        fd = os.open(self.path, os.O_RDONLY | os.O_NOFOLLOW)
        with os.fdopen(fd, "r", encoding="ascii") as source:
            info = os.fstat(source.fileno())
            if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) != 0o600:
                raise ValueError("the management credential must be an owner-only regular file (mode 600)")
            token = source.read(66).strip()
        if not MANAGEMENT_TOKEN.fullmatch(token):
            raise ValueError("the management credential is invalid; restore it or rotate it locally")
        return token

    def rotate(self) -> None:
        private_directory(self.path.parent)
        if self.path.exists() or self.path.is_symlink():
            self.read()
        fd, name = tempfile.mkstemp(prefix=".management-token-", dir=self.path.parent)
        temporary = Path(name)
        try:
            with os.fdopen(fd, "w", encoding="ascii") as output:
                output.write(secrets.token_hex(32) + "\n")
                output.flush()
                os.fsync(output.fileno())
            temporary.replace(self.path)
        finally:
            temporary.unlink(missing_ok=True)

    def authorizes(self, headers) -> bool:
        values = headers.get_all("Authorization", [])
        if len(values) != 1 or not values[0].startswith("Bearer "):
            return False
        supplied = values[0][7:]
        if not MANAGEMENT_TOKEN.fullmatch(supplied):
            return False
        try:
            expected = self.read()
        except (OSError, ValueError, UnicodeError):
            return False
        return hmac.compare_digest(supplied, expected)


# ---------------------------------------------------------------------------
# Reading a signed app


def read_app_identity(info: dict) -> dict:
    try:
        return {
            "bundleIdentifier": str(info["CFBundleIdentifier"]),
            "version": str(info["CFBundleShortVersionString"]),
            "build": str(info["CFBundleVersion"]),
            "title": str(info.get("CFBundleDisplayName") or info["CFBundleName"]),
        }
    except (KeyError, TypeError) as error:
        raise ValueError(f"app Info.plist is missing {error}") from error


def icon_candidates(info: dict) -> list:
    """Icon file name prefixes from Info.plist, largest-looking first."""
    names = []
    for key in ("CFBundleIcons", "CFBundleIcons~ipad"):
        primary = (info.get(key) or {}).get("CFBundlePrimaryIcon") or {}
        names.extend(primary.get("CFBundleIconFiles") or [])
    return sorted(set(names), key=lambda name: [int(n) for n in re.findall(r"\d+", name)] or [0], reverse=True)


def pick_icon(names: list, prefixes: list) -> Optional[str]:
    """Choose the largest bundled PNG whose name starts with an Info.plist icon prefix."""
    best = None
    best_score = -1.0
    for name in names:
        base = name.rsplit("/", 1)[-1]
        if not base.lower().endswith(".png"):
            continue
        if not any(base.startswith(prefix) for prefix in prefixes):
            continue
        size = re.findall(r"(\d+(?:\.\d+)?)x\1", base)
        scale = re.findall(r"@(\d)x", base)
        score = (float(size[0]) if size else 1.0) * (int(scale[0]) if scale else 1)
        if score > best_score:
            best, best_score = name, score
    return best


def provisioning_summary(raw: bytes) -> dict:
    """Read the unsigned plist inside embedded.mobileprovision (a CMS envelope)."""
    start = raw.find(b"<?xml")
    end = raw.find(b"</plist>")
    if start < 0 or end < 0:
        return {}
    try:
        profile = plistlib.loads(raw[start : end + len(b"</plist>")])
    except plistlib.InvalidFileException:
        return {}
    expires = profile.get("ExpirationDate")
    entitlements = profile.get("Entitlements") or {}
    if profile.get("ProvisionsAllDevices"):
        kind = "enterprise"
    elif entitlements.get("get-task-allow"):
        kind = "development"
    else:
        kind = "ad-hoc"
    return {
        "profileName": str(profile.get("Name", "")),
        "profileKind": kind,
        "profileExpires": expires.replace(tzinfo=timezone.utc).isoformat().replace("+00:00", "Z")
        if isinstance(expires, datetime)
        else None,
        "provisionedDeviceCount": len(profile.get("ProvisionedDevices") or []),
    }


def zip_app(app: Path, destination: Path) -> None:
    """Write `Payload/<name>.app` into an IPA, keeping symlinks and executable bits."""
    with zipfile.ZipFile(destination, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=6) as archive:
        root = Path("Payload") / app.name
        for directory, subdirectories, files in os.walk(app, followlinks=False):
            subdirectories.sort()
            current = Path(directory)
            relative = root / current.relative_to(app)
            entry = zipfile.ZipInfo(str(relative) + "/")
            entry.external_attr = (stat.S_IFDIR | 0o755) << 16
            archive.writestr(entry, b"")
            for name in sorted(files) + [d for d in subdirectories if (current / d).is_symlink()]:
                path = current / name
                member = str(relative / name)
                if path.is_symlink():
                    entry = zipfile.ZipInfo(member)
                    entry.create_system = 3
                    entry.external_attr = (stat.S_IFLNK | 0o755) << 16
                    archive.writestr(entry, os.readlink(path))
                else:
                    archive.write(path, member)


def inspect_ipa(ipa: Path) -> tuple:
    """Return (identity, provisioning summary, icon bytes or None) from an IPA."""
    with zipfile.ZipFile(ipa) as archive:
        names = archive.namelist()
        info_members = [
            name
            for name in names
            if name.startswith("Payload/") and name.endswith(".app/Info.plist") and name.count("/") == 2
        ]
        if len(info_members) != 1:
            raise ValueError(f"expected one app in {ipa.name}, found {len(info_members)}")
        app_prefix = info_members[0][: -len("Info.plist")]
        info = plistlib.loads(archive.read(info_members[0]))
        identity = read_app_identity(info)
        profile_member = app_prefix + "embedded.mobileprovision"
        if profile_member not in names:
            raise ValueError("the app has no embedded.mobileprovision; build it signed for a device")
        provisioning = provisioning_summary(archive.read(profile_member))
        top_level = [name for name in names if name.startswith(app_prefix) and name.count("/") == 2]
        icon_name = pick_icon(top_level, icon_candidates(info))
        icon = archive.read(icon_name) if icon_name else None
    return identity, provisioning, icon


# ---------------------------------------------------------------------------
# The queue on disk


class Queue:
    def __init__(self, root: Path):
        self.root = root
        self.lock = threading.Lock()
        # Downloads in flight, by a per-request token. In memory only: a restart ends them anyway.
        self.active: dict = {}

    def release_dir(self, release_id: str) -> Path:
        if not RELEASE_ID.match(release_id):
            raise KeyError(release_id)
        directory = self.root / release_id
        if not (directory / "release.json").is_file():
            raise KeyError(release_id)
        return directory

    def load(self, release_id: str) -> dict:
        directory = self.release_dir(release_id)
        record = json.loads((directory / "release.json").read_text(encoding="utf-8"))
        record["id"] = release_id
        record["downloads"] = self.downloads(directory)
        return record

    def downloads(self, directory: Path) -> list:
        log = directory / "downloads.jsonl"
        if not log.is_file():
            return []
        events = []
        for line in log.read_text(encoding="utf-8").splitlines():
            try:
                events.append(json.loads(line))
            except json.JSONDecodeError:
                continue
        return events

    def records(self) -> list:
        if not self.root.is_dir():
            return []
        found = []
        for directory in self.root.iterdir():
            if RELEASE_ID.match(directory.name) and (directory / "release.json").is_file():
                try:
                    found.append(self.load(directory.name))
                except (OSError, ValueError, KeyError):
                    continue
        return sorted(found, key=lambda record: record["id"], reverse=True)

    def record_download(self, release_id: str, event: dict) -> None:
        directory = self.release_dir(release_id)
        with self.lock:
            with (directory / "downloads.jsonl").open("a", encoding="utf-8") as log:
                log.write(json.dumps(event, sort_keys=True) + "\n")
            if event.get("complete"):
                record = json.loads((directory / "release.json").read_text(encoding="utf-8"))
                devices = self._devices_unlocked()
                key = device_key(event.get("deviceId"), event.get("family") or device_kind(event.get("userAgent", "")))
                entry = devices.setdefault(key, {"family": event.get("family") or device_kind(event.get("userAgent", "")), "apps": {}})
                entry["apps"][record["bundleIdentifier"]] = {
                    "releaseId": release_id,
                    "title": record["title"],
                    "version": record["version"],
                    "build": record["build"],
                    "at": event["at"],
                }
                self._write_devices(devices)

    # What each device last downloaded, kept apart from the releases so it
    # outlives them: `{device key: {"family": "iPhone", "apps": {bundle id: {...}}}}`.

    def devices_path(self) -> Path:
        return self.root / "devices.json"

    def devices(self) -> dict:
        with self.lock:
            return self._devices_unlocked()

    def _devices_unlocked(self) -> dict:
        path = self.devices_path()
        if path.is_file():
            try:
                return json.loads(path.read_text(encoding="utf-8"))
            except (OSError, json.JSONDecodeError):
                pass
        # First run (or a damaged file): rebuild from the download logs, which only name the device family.
        devices: dict = {}
        events = []
        for record in self.records():
            for event in record["downloads"]:
                if event.get("complete"):
                    events.append((event["at"], record, event))
        for at, record, event in sorted(events, key=lambda item: item[0]):
            family = event.get("family") or device_kind(event.get("userAgent", ""))
            entry = devices.setdefault(device_key(event.get("deviceId"), family), {"family": family, "apps": {}})
            entry["apps"][record["bundleIdentifier"]] = {
                "releaseId": record["id"],
                "title": record["title"],
                "version": record["version"],
                "build": record["build"],
                "at": at,
            }
        if self.root.is_dir():
            self._write_devices(devices)
        return devices

    def _write_devices(self, devices: dict) -> None:
        temporary = self.devices_path().with_suffix(".tmp")
        temporary.write_text(json.dumps(devices, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        temporary.replace(self.devices_path())

    def installed_on(self, device_id: Optional[str], family: Optional[str]) -> dict:
        """The newest build of each app this device downloaded, by its own id or (older records) its family."""
        devices = self.devices()
        merged: dict = {}
        keys = [key for key in (device_key(device_id, None), device_key(None, family)) if key]
        for key in keys:
            for bundle, app in (devices.get(key) or {}).get("apps", {}).items():
                if bundle not in merged or app["at"] > merged[bundle]["at"]:
                    merged[bundle] = app
        return merged

    def start_download(self, token: object, release_id: str, device_id: Optional[str], family: str, total: int) -> None:
        with self.lock:
            self.active[token] = {"releaseId": release_id, "deviceId": device_id, "family": family, "bytes": 0, "totalBytes": total, "startedAt": utc_now()}

    def progress(self, token: object, sent: int) -> None:
        with self.lock:
            if token in self.active:
                self.active[token]["bytes"] = sent

    def finish_download(self, token: object) -> None:
        with self.lock:
            self.active.pop(token, None)

    def active_downloads(self, release_id: str) -> list:
        with self.lock:
            return [dict(item) for item in self.active.values() if item["releaseId"] == release_id]

    def publish(self, source: Path, note: str = "", keep: int = DEFAULT_KEEP) -> dict:
        source = source.expanduser().resolve()
        private_directory(self.root)
        staging = Path(tempfile.mkdtemp(prefix=".publish-", dir=self.root))
        try:
            ipa = staging / "app.ipa"
            if source.is_dir() and source.suffix == ".app":
                zip_app(source, ipa)
            elif source.is_file() and source.suffix == ".ipa":
                shutil.copyfile(source, ipa)
            else:
                raise ValueError(f"expected a signed .app folder or an .ipa file: {source}")
            identity, provisioning, icon = inspect_ipa(ipa)
            if icon:
                (staging / "icon.png").write_bytes(icon)
            digest = hashlib.sha256()
            with ipa.open("rb") as handle:
                for block in iter(lambda: handle.read(CHUNK), b""):
                    digest.update(block)
            created = datetime.now(timezone.utc)
            release_id = created.strftime("%Y%m%dT%H%M%SZ") + "-" + digest.hexdigest()[:8]
            record = {
                "schemaVersion": SCHEMA_VERSION,
                **identity,
                **provisioning,
                "note": note,
                "createdAt": created.replace(microsecond=0).isoformat().replace("+00:00", "Z"),
                "ipaBytes": ipa.stat().st_size,
                "ipaSHA256": digest.hexdigest(),
                "hasIcon": bool(icon),
            }
            (staging / "release.json").write_text(json.dumps(record, indent=2, sort_keys=True) + "\n", encoding="utf-8")
            final = self.root / release_id
            if final.exists():
                raise ValueError(f"release {release_id} already exists")
            staging.rename(final)
        except BaseException:
            shutil.rmtree(staging, ignore_errors=True)
            raise
        self.prune(record["bundleIdentifier"], keep)
        self.prune_expired()
        return self.load(release_id)

    def prune(self, bundle_identifier: str, keep: int) -> list:
        """Keep the newest `keep` releases of one app; move the rest out of the queue."""
        same_app = [record for record in self.records() if record.get("bundleIdentifier") == bundle_identifier]
        removed = []
        for record in same_app[max(keep, 1) :]:
            self.remove(record["id"])
            removed.append(record["id"])
        return removed

    def prune_expired(self, now: Optional[datetime] = None) -> list:
        """Remove releases whose provisioning profile has expired; iOS would refuse them anyway."""
        stamp = (now or datetime.now(timezone.utc)).replace(microsecond=0).isoformat().replace("+00:00", "Z")
        removed = []
        for record in self.records():
            expires = record.get("profileExpires")
            if expires and expires <= stamp:
                self.remove(record["id"])
                removed.append(record["id"])
        return removed

    def remove(self, release_id: str) -> None:
        shutil.rmtree(self.release_dir(release_id))


# ---------------------------------------------------------------------------
# Serving


def manifest_plist(record: dict, base_url: str, tag: str = "") -> bytes:
    release = f"{base_url}r/{record['id']}/"
    assets = [{"kind": "software-package", "url": release + "app.ipa" + tag}]
    if record.get("hasIcon"):
        assets.append({"kind": "display-image", "url": release + "icon.png"})
        assets.append({"kind": "full-size-image", "url": release + "icon.png"})
    manifest = {
        "items": [
            {
                "assets": assets,
                "metadata": {
                    "bundle-identifier": record["bundleIdentifier"],
                    "bundle-version": record["version"],
                    "kind": "software",
                    "title": record["title"],
                },
            }
        ]
    }
    return plistlib.dumps(manifest, fmt=plistlib.FMT_XML, sort_keys=True)


def device_key(device_id: Optional[str], family: Optional[str]) -> Optional[str]:
    if device_id and DEVICE_ID.match(device_id):
        return "id:" + device_id
    if family:
        return "family:" + family
    return None


def device_tag(query: str) -> tuple:
    """Read `device` and `family` from an install link's query; drop anything malformed."""
    values = parse_qs(query)
    device_id = (values.get("device") or [""])[0]
    family = (values.get("family") or [""])[0]
    return (device_id if DEVICE_ID.match(device_id) else None, family if family in FAMILIES else None)


def tag_query(device_id: Optional[str], family: Optional[str]) -> str:
    pairs = [(key, value) for key, value in (("device", device_id), ("family", family)) if value]
    return "?" + urlencode(pairs) if pairs else ""


def device_kind(user_agent: str) -> str:
    """Name the device family from iOS's installer user agent (`... model/iPad13,18 ...`)."""
    match = re.search(r"model/([A-Za-z]+)", user_agent or "")
    return match.group(1) if match else "Unknown device"


def downloads_by_device(completed: list) -> list:
    """The latest completed download per device family, newest first."""
    latest = {}
    for event in completed:
        latest[event.get("family") or device_kind(event.get("userAgent", ""))] = event["at"]
    return [{"device": device, "at": at} for device, at in sorted(latest.items(), key=lambda item: item[1], reverse=True)]


def public_record(record: dict, base_url: str, active: Optional[list] = None) -> dict:
    release = f"{base_url}r/{record['id']}/"
    manifest_url = release + "manifest.plist"
    downloads = record.get("downloads") or []
    completed = [event for event in downloads if event.get("complete")]
    return {
        "id": record["id"],
        "title": record["title"],
        "bundleIdentifier": record["bundleIdentifier"],
        "version": record["version"],
        "build": record["build"],
        "note": record.get("note") or "",
        "createdAt": record["createdAt"],
        "ipaBytes": record["ipaBytes"],
        "profileKind": record.get("profileKind"),
        "profileExpires": record.get("profileExpires"),
        "iconURL": release + "icon.png" if record.get("hasIcon") else None,
        "manifestURL": manifest_url,
        "installURL": "itms-services://?action=download-manifest&url=" + quote(manifest_url, safe=""),
        "downloadCount": len(completed),
        "lastDownloadedAt": completed[-1]["at"] if completed else None,
        "downloadedBy": downloads_by_device(completed),
        "activeDownloads": [
            {"deviceId": item["deviceId"], "device": item["family"], "bytes": item["bytes"], "totalBytes": item["totalBytes"], "startedAt": item["startedAt"]}
            for item in active or []
        ],
    }


def index_page(records: list) -> bytes:
    rows = []
    for record in records:
        downloaded = (
            "Downloaded on " + ", ".join(html.escape(f"{entry['device']} {entry['at']}") for entry in record["downloadedBy"])
            if record["downloadedBy"]
            else "Not downloaded yet"
        )
        icon = (
            f'<img src="{html.escape(record["iconURL"], quote=True)}" alt="">' if record["iconURL"] else '<div class="blank"></div>'
        )
        note = f"<p>{html.escape(record['note'])}</p>" if record["note"] else ""
        rows.append(
            f"""<li>{icon}<div class="text"><h2>{html.escape(record['title'])}</h2>
<p>{html.escape(record['version'])} ({html.escape(record['build'])}) · {html.escape(record['createdAt'])}</p>{note}
<p class="muted">{downloaded}</p></div>
<a class="button" href="{html.escape(record['installURL'], quote=True)}">Install</a></li>"""
        )
    body = "\n".join(rows) or "<li><p>No builds are waiting.</p></li>"
    page = f"""<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Deploy Link builds</title>
<style>
body {{ background: #f4f6fa; color: #172033; font: 17px -apple-system, system-ui, sans-serif; margin: 0; padding: 24px 16px; }}
h1 {{ font-size: 28px; margin: 0 auto 16px; max-width: 560px; }}
ul {{ list-style: none; margin: 0 auto; max-width: 560px; padding: 0; }}
li {{ align-items: center; background: white; border-radius: 18px; display: flex; gap: 14px; margin-bottom: 12px; padding: 14px; }}
img, .blank {{ background: #dfe3ec; border-radius: 13px; height: 56px; width: 56px; }}
.text {{ flex: 1; min-width: 0; }} h2 {{ font-size: 18px; margin: 0; }} p {{ color: #566179; margin: 2px 0; }}
.muted {{ font-size: 14px; }}
a.button {{ background: #225cbf; border-radius: 12px; color: white; font-weight: 650; padding: 10px 16px; text-decoration: none; }}
</style></head>
<body><h1>Deploy Link builds</h1><ul>
{body}
</ul></body></html>
"""
    return page.encode("utf-8")


def make_handler(queue: Queue, base_url: str):
    credential = ManagementCredential(queue.root)
    credential.ensure()

    class Handler(BaseHTTPRequestHandler):
        server_version = "DeployLinkOTA/1"

        def log_message(self, format: str, *args) -> None:  # noqa: A002 - stdlib signature
            # BaseHTTPRequestHandler includes raw request lines in these args.
            # Never log headers, URLs, or malformed input that could contain a key.
            sys.stderr.write(f"{utc_now()} HTTP request error\n")

        def log_request(self, code="-", size="-") -> None:
            method = self.command if self.command in ("GET", "HEAD", "DELETE") else "OTHER"
            status = str(code) if str(code).isdigit() else "-"
            sys.stderr.write(f"{utc_now()} {method} {status}\n")

        def send_bytes(self, status: int, content_type: str, payload: bytes, head: bool = False) -> None:
            self.send_response(status)
            self.send_header("Content-Type", content_type)
            self.send_header("Content-Length", str(len(payload)))
            self.send_header("Cache-Control", "no-store")
            self.end_headers()
            if not head:
                self.wfile.write(payload)

        def not_found(self, head: bool = False) -> None:
            self.send_bytes(404, "application/json", b'{"error":"not found"}\n', head)

        def require_management(self, head: bool = False) -> bool:
            if credential.authorizes(self.headers):
                return True
            payload = b'{"error":"management token required"}\n'
            self.send_response(401)
            self.send_header("WWW-Authenticate", 'Bearer realm="Deploy Link management"')
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(payload)))
            self.send_header("Cache-Control", "no-store")
            self.end_headers()
            if not head:
                self.wfile.write(payload)
            return False

        def do_HEAD(self) -> None:  # noqa: N802 - stdlib naming
            self.route(head=True)

        def do_GET(self) -> None:  # noqa: N802 - stdlib naming
            self.route(head=False)

        def do_DELETE(self) -> None:  # noqa: N802 - stdlib naming
            parts = urlparse(self.path).path.strip("/").split("/")
            if len(parts) != 3 or parts[:2] != ["api", "releases"]:
                return self.not_found()
            if not self.require_management():
                return
            try:
                queue.remove(parts[2])
            except (KeyError, OSError):
                return self.not_found()
            self.send_bytes(200, "application/json", b'{"removed":true}\n')

        def releases(self) -> list:
            queue.prune_expired()
            return [public_record(record, base_url, queue.active_downloads(record["id"])) for record in queue.records()]

        def route(self, head: bool) -> None:
            url = urlparse(self.path)
            path = url.path
            if path == "/api/management":
                if self.require_management(head):
                    self.send_bytes(200, "application/json", b'{"canDelete":true}\n', head)
                return
            if path in ("/", "/index.html"):
                return self.send_bytes(200, "text/html; charset=utf-8", index_page(self.releases()), head)
            if path == "/api/releases":
                releases = self.releases()
                available = {release["id"] for release in releases}
                device_id, family = device_tag(url.query)
                installed = {
                    bundle: {**app, "available": app["releaseId"] in available}
                    for bundle, app in queue.installed_on(device_id, family).items()
                }
                payload = {
                    "schemaVersion": SCHEMA_VERSION,
                    "generatedAt": utc_now(),
                    "releases": releases,
                    "installed": installed,
                }
                return self.send_bytes(200, "application/json", json.dumps(payload, sort_keys=True).encode("utf-8"), head)
            parts = path.strip("/").split("/")
            if len(parts) != 3 or parts[0] != "r":
                return self.not_found(head)
            release_id, name = parts[1], parts[2]
            try:
                record = queue.load(release_id)
                directory = queue.release_dir(release_id)
            except (KeyError, OSError, ValueError):
                return self.not_found(head)
            device_id, family = device_tag(url.query)
            if name == "manifest.plist":
                return self.send_bytes(200, "text/xml", manifest_plist(record, base_url, tag_query(device_id, family)), head)
            if name == "icon.png" and record.get("hasIcon"):
                return self.send_bytes(200, "image/png", (directory / "icon.png").read_bytes(), head)
            if name == "app.ipa":
                return self.send_ipa(release_id, directory / "app.ipa", head, device_id, family)
            return self.not_found(head)

        def send_ipa(self, release_id: str, ipa: Path, head: bool, device_id: Optional[str], family: Optional[str]) -> None:
            size = ipa.stat().st_size
            self.send_response(200)
            self.send_header("Content-Type", "application/octet-stream")
            self.send_header("Content-Length", str(size))
            self.send_header("Cache-Control", "no-store")
            self.end_headers()
            if head:
                return
            sent = 0
            started = utc_now()
            user_agent = (self.headers.get("User-Agent") or "")[:200]
            family = family or device_kind(user_agent)
            token = object()
            queue.start_download(token, release_id, device_id, family, size)
            try:
                with ipa.open("rb") as handle:
                    for block in iter(lambda: handle.read(CHUNK), b""):
                        self.wfile.write(block)
                        sent += len(block)
                        queue.progress(token, sent)
            except (BrokenPipeError, ConnectionResetError):
                pass
            finally:
                queue.finish_download(token)
                event = {
                    "at": utc_now(),
                    "startedAt": started,
                    "bytes": sent,
                    "complete": sent == size,
                    "family": family,
                    "userAgent": user_agent,
                }
                if device_id:
                    event["deviceId"] = device_id
                try:
                    queue.record_download(release_id, event)
                except KeyError:
                    pass  # removed while it downloaded

    return Handler


def normalized_base_url(value: str) -> str:
    parsed = urlparse(value)
    if (parsed.scheme != "https" or not parsed.hostname or parsed.username is not None
            or parsed.password is not None or parsed.query or parsed.fragment
            or parsed.path not in ("", "/") or parsed.port not in (None, 443)):
        raise ValueError("--base-url must be a root https URL without credentials, a path, query, or fragment")
    return value.rstrip("/") + "/"


def serve_route_state(config: dict, base_url: str, port: int) -> str:
    """Refuse a conflicting root/HTTPS port; leave all unrelated Serve routes alone."""
    if not isinstance(config, dict):
        raise ValueError("could not read the Tailscale Serve configuration")
    hostname = urlparse(normalized_base_url(base_url)).hostname
    endpoint = f"{hostname}:443"
    if (config.get("AllowFunnel") or {}).get(endpoint):
        raise ValueError("Tailscale Funnel is enabled on this HTTPS endpoint; Deploy Link requires a private tailnet endpoint")
    tcp = (config.get("TCP") or {}).get("443")
    if tcp and (tcp.get("HTTPS") is not True or tcp.get("TCPForward")):
        raise ValueError("Tailscale port 443 is already used by a different service")
    handlers = (((config.get("Web") or {}).get(endpoint) or {}).get("Handlers") or {})
    # These paths would shadow the queue even if its root route were available.
    for path in handlers:
        if path == "/index.html" or path.rstrip("/") in ("/api", "/r") or path.startswith(("/api/", "/r/")):
            raise ValueError("a Tailscale Serve route already uses a Deploy Link API or download path")
    root = handlers.get("/")
    if root is None:
        return "configure"
    proxy = root.get("Proxy")
    if len(root) == 1 and proxy in (f"http://127.0.0.1:{port}", f"http://localhost:{port}"):
        return "existing"
    raise ValueError("Tailscale HTTPS already has a different root endpoint; choose a separate host or move that route before installing")


# ---------------------------------------------------------------------------
# Commands


def command_publish(args: argparse.Namespace) -> None:
    record = Queue(args.root).publish(Path(args.source), note=args.note, keep=args.keep)
    print(json.dumps({key: record[key] for key in ("id", "title", "bundleIdentifier", "version", "build", "profileKind", "provisionedDeviceCount", "ipaBytes") if key in record}, sort_keys=True))


def command_list(args: argparse.Namespace) -> None:
    for record in Queue(args.root).records():
        completed = [event for event in record["downloads"] if event.get("complete")]
        print(json.dumps({
            "id": record["id"],
            "title": record["title"],
            "bundleIdentifier": record["bundleIdentifier"],
            "version": record["version"],
            "build": record["build"],
            "note": record.get("note") or "",
            "createdAt": record["createdAt"],
            "downloadCount": len(completed),
            "downloadedBy": downloads_by_device(completed),
        }, sort_keys=True))


def command_prune(args: argparse.Namespace) -> None:
    """Keep the newest `--keep` releases of every app."""
    queue = Queue(args.root)
    removed = queue.prune_expired()
    for bundle in sorted({record["bundleIdentifier"] for record in queue.records()}):
        removed += queue.prune(bundle, args.keep)
    print(json.dumps({"removed": removed, "kept": len(queue.records())}, sort_keys=True))


def command_devices(args: argparse.Namespace) -> None:
    print(json.dumps(Queue(args.root).devices(), indent=2, sort_keys=True))


def command_remove(args: argparse.Namespace) -> None:
    try:
        Queue(args.root).remove(args.release_id)
    except KeyError:
        raise SystemExit(f"error: no release {args.release_id}")


def command_serve(args: argparse.Namespace) -> None:
    base_url = normalized_base_url(args.base_url)
    queue = Queue(args.root)
    server = ThreadingHTTPServer(("127.0.0.1", args.port), make_handler(queue, base_url))
    server.daemon_threads = True
    print(f"Serving {args.root} on http://127.0.0.1:{args.port} as {base_url}", flush=True)
    server.serve_forever()


def command_admin_token(args: argparse.Namespace) -> None:
    credential = ManagementCredential(args.root)
    if args.rotate:
        credential.rotate()
    else:
        credential.ensure()
    if args.copy:
        if sys.platform != "darwin":
            raise ValueError("clipboard copy requires macOS")
        subprocess.run(["/usr/bin/pbcopy"], input=credential.read().encode("ascii"), check=True)
        print("Management token copied. Paste it into Deploy Link Settings; it grants queue deletion access.")
    else:
        print("Management credential ready. Use admin-token --copy to copy it locally without displaying it.")


def command_check_serve(args: argparse.Namespace) -> None:
    print(serve_route_state(json.load(sys.stdin) or {}, args.base_url, args.port))


def parser() -> argparse.ArgumentParser:
    root = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    root.add_argument("--root", type=lambda value: Path(value).expanduser(), default=DEFAULT_ROOT, help=f"queue folder (default {DEFAULT_ROOT})")
    commands = root.add_subparsers(dest="command", required=True)

    publish = commands.add_parser("publish", help="queue a signed .app or .ipa")
    publish.add_argument("source")
    publish.add_argument("--note", default="", help="short line shown under the build")
    publish.add_argument("--keep", type=int, default=DEFAULT_KEEP, help="newest releases kept per app")
    publish.set_defaults(action=command_publish)

    listing = commands.add_parser("list", help="print queued releases, newest first")
    listing.set_defaults(action=command_list)

    prune = commands.add_parser("prune", help="keep only the newest releases of every app")
    prune.add_argument("--keep", type=int, default=1, help="newest releases kept per app (default 1)")
    prune.set_defaults(action=command_prune)

    devices = commands.add_parser("devices", help="print the last build of each app every device downloaded")
    devices.set_defaults(action=command_devices)

    remove = commands.add_parser("remove", help="delete one queued release")
    remove.add_argument("release_id")
    remove.set_defaults(action=command_remove)

    serve = commands.add_parser("serve", help="serve the queue on loopback for tailscale serve")
    serve.add_argument("--base-url", required=True, help="the tailnet HTTPS address that proxies to this port")
    serve.add_argument("--port", type=int, default=DEFAULT_PORT)
    serve.set_defaults(action=command_serve)

    management = commands.add_parser("admin-token", help="create, copy, or rotate the local queue-management credential; never prints it")
    management.add_argument("--copy", action="store_true", help="copy the token to the macOS clipboard")
    management.add_argument("--rotate", action="store_true", help="revoke existing management credentials and create a new one")
    management.set_defaults(action=command_admin_token)

    check_serve = commands.add_parser("check-serve", help="check for conflicting Tailscale routes; reads Serve status JSON on stdin")
    check_serve.add_argument("--base-url", required=True)
    check_serve.add_argument("--port", type=int, default=DEFAULT_PORT)
    check_serve.set_defaults(action=command_check_serve)
    return root


def main() -> None:
    args = parser().parse_args()
    try:
        args.action(args)
    except (OSError, ValueError, plistlib.InvalidFileException, zipfile.BadZipFile, subprocess.CalledProcessError) as error:
        raise SystemExit(f"error: {error}") from error


if __name__ == "__main__":
    main()
