#!/usr/bin/env python3
"""Prepare and serve one private Apple OTA development release."""

from __future__ import annotations

import argparse
import binascii
import hashlib
import html
import json
import mimetypes
import plistlib
import struct
import zipfile
import zlib
from dataclasses import dataclass
from datetime import datetime, timezone
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import quote, urljoin, urlparse


@dataclass(frozen=True)
class ReleaseURLs:
    base: str
    ipa: str
    manifest: str
    display_image: str
    full_size_image: str

    @classmethod
    def make(cls, base_url: str, ipa_name: str) -> "ReleaseURLs":
        parsed = urlparse(base_url)
        if parsed.scheme != "https" or not parsed.netloc or parsed.query or parsed.fragment:
            raise ValueError("base URL must be a fully qualified HTTPS URL without a query or fragment")
        normalized = base_url.rstrip("/") + "/"
        return cls(
            base=normalized,
            ipa=urljoin(normalized, ipa_name),
            manifest=urljoin(normalized, "manifest.plist"),
            display_image=urljoin(normalized, "icon-57.png"),
            full_size_image=urljoin(normalized, "icon-512.png"),
        )


def write_export_options(args: argparse.Namespace) -> None:
    urls = ReleaseURLs.make(args.base_url, args.ipa_name)
    options = {
        "destination": "export",
        "distributionBundleIdentifier": args.bundle_id,
        "manageAppVersionAndBuildNumber": False,
        "manifest": {
            "appURL": urls.ipa,
            "displayImageURL": urls.display_image,
            "fullSizeImageURL": urls.full_size_image,
        },
        "method": args.method,
        "signingStyle": "automatic",
        "teamID": args.team_id,
        "thinning": "<none>",
    }
    destination = Path(args.output).expanduser().resolve()
    destination.parent.mkdir(parents=True, exist_ok=True)
    with destination.open("wb") as handle:
        plistlib.dump(options, handle, fmt=plistlib.FMT_XML, sort_keys=True)
    print(destination)


def png_chunk(kind: bytes, payload: bytes) -> bytes:
    checksum = binascii.crc32(kind)
    checksum = binascii.crc32(payload, checksum) & 0xFFFFFFFF
    return struct.pack(">I", len(payload)) + kind + payload + struct.pack(">I", checksum)


def write_placeholder_icon(destination: Path, size: int) -> None:
    background = bytes((34, 92, 191, 255))
    foreground = bytes((255, 255, 255, 255))
    inset = max(3, size // 5)
    rows = []
    for y in range(size):
        row = bytearray()
        for x in range(size):
            inside = inset <= x < size - inset and inset <= y < size - inset
            row.extend(foreground if inside else background)
        rows.append(b"\x00" + bytes(row))
    payload = b"".join(rows)
    png = (
        b"\x89PNG\r\n\x1a\n"
        + png_chunk(b"IHDR", struct.pack(">IIBBBBB", size, size, 8, 6, 0, 0, 0))
        + png_chunk(b"IDAT", zlib.compress(payload, level=9))
        + png_chunk(b"IEND", b"")
    )
    destination.write_bytes(png)


def read_manifest(directory: Path) -> dict:
    manifest_path = directory / "manifest.plist"
    if not manifest_path.is_file():
        raise ValueError(f"missing Xcode manifest: {manifest_path}")
    with manifest_path.open("rb") as handle:
        manifest = plistlib.load(handle)
    if not isinstance(manifest, dict):
        raise ValueError("Xcode manifest is not a dictionary")
    return manifest


def manifest_metadata(manifest: dict) -> tuple[str, str, str, str]:
    try:
        item = manifest["items"][0]
        metadata = item["metadata"]
        assets = item["assets"]
        package = next(asset for asset in assets if asset["kind"] == "software-package")
        return (
            str(metadata["bundle-identifier"]),
            str(metadata["bundle-version"]),
            str(metadata["title"]),
            str(package["url"]),
        )
    except (IndexError, KeyError, StopIteration, TypeError) as error:
        raise ValueError("Xcode manifest is missing required OTA fields") from error


def ipa_metadata(ipa: Path) -> tuple[str, str, str, str]:
    with zipfile.ZipFile(ipa) as archive:
        info_members = [
            name
            for name in archive.namelist()
            if name.startswith("Payload/")
            and name.endswith(".app/Info.plist")
            and name.count("/") == 2
        ]
        if len(info_members) != 1:
            raise ValueError(f"expected one app Info.plist in {ipa.name}, found {len(info_members)}")
        info = plistlib.loads(archive.read(info_members[0]))

    try:
        return (
            str(info["CFBundleIdentifier"]),
            str(info["CFBundleShortVersionString"]),
            str(info["CFBundleVersion"]),
            str(info.get("CFBundleDisplayName") or info["CFBundleName"]),
        )
    except (KeyError, TypeError) as error:
        raise ValueError("app Info.plist is missing required release identity fields") from error


def finish_release(args: argparse.Namespace) -> None:
    directory = Path(args.directory).expanduser().resolve()
    if not directory.is_dir():
        raise ValueError(f"release directory does not exist: {directory}")

    ipa_files = sorted(directory.glob("*.ipa"))
    if len(ipa_files) != 1:
        raise ValueError(f"expected exactly one IPA in {directory}, found {len(ipa_files)}")
    ipa = ipa_files[0]
    urls = ReleaseURLs.make(args.base_url, ipa.name)
    manifest = read_manifest(directory)
    manifest_bundle_id, manifest_version, _, package_url = manifest_metadata(manifest)
    bundle_id, marketing_version, build_number, title = ipa_metadata(ipa)
    if manifest_bundle_id != bundle_id:
        raise ValueError(
            f"manifest bundle identifier is {manifest_bundle_id}, but the IPA uses {bundle_id}"
        )
    if manifest_version != marketing_version:
        raise ValueError(
            f"manifest version is {manifest_version}, but the IPA marketing version is {marketing_version}"
        )
    if package_url != urls.ipa:
        raise ValueError(f"manifest IPA URL is {package_url}, expected {urls.ipa}")

    manifest["items"][0]["metadata"]["title"] = title
    with (directory / "manifest.plist").open("wb") as handle:
        plistlib.dump(manifest, handle, fmt=plistlib.FMT_XML, sort_keys=True)

    write_placeholder_icon(directory / "icon-57.png", 57)
    write_placeholder_icon(directory / "icon-512.png", 512)

    encoded_manifest = quote(urls.manifest, safe="")
    install_url = f"itms-services://?action=download-manifest&url={encoded_manifest}"
    safe_title = html.escape(title)
    safe_version = html.escape(f"{marketing_version} ({build_number})")
    safe_install_url = html.escape(install_url, quote=True)
    page = f"""<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Install {safe_title}</title>
  <style>
    body {{ background: #f4f6fa; color: #172033; font: 17px -apple-system, system-ui, sans-serif; margin: 0; }}
    main {{ background: white; border-radius: 22px; box-shadow: 0 12px 40px #17203318; margin: 12vh auto; max-width: 520px; padding: 34px; }}
    h1 {{ font-size: 28px; margin: 0 0 8px; }}
    p {{ color: #566179; line-height: 1.45; }}
    a.button {{ background: #225cbf; border-radius: 14px; color: white; display: block; font-weight: 650; margin-top: 26px; padding: 15px; text-align: center; text-decoration: none; }}
  </style>
</head>
<body>
  <main>
    <h1>{safe_title}</h1>
    <p>Private registered-device build {safe_version}. Keep Tailscale connected until installation finishes.</p>
    <a class="button" href="{safe_install_url}">Install on this iPhone</a>
  </main>
</body>
</html>
"""
    (directory / "index.html").write_text(page, encoding="utf-8")

    digest = hashlib.sha256(ipa.read_bytes()).hexdigest()
    release_record = {
        "schemaVersion": 2,
        "dataClass": "private-ios-ota-registered-device-build",
        "bundleIdentifier": bundle_id,
        "marketingVersion": marketing_version,
        "buildNumber": build_number,
        "title": title,
        "ipaFilename": ipa.name,
        "ipaSHA256": digest,
        "installPageURL": urls.base,
        "createdAt": datetime.now(timezone.utc).isoformat(),
        "retention": "immutable until the build is confirmed superseded, then move this exact release directory to Trash",
    }
    (directory / "release.json").write_text(
        json.dumps(release_record, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    print(json.dumps({"installPage": urls.base, "installURL": install_url}, indent=2))


class OTARequestHandler(SimpleHTTPRequestHandler):
    extensions_map = {
        **mimetypes.types_map,
        ".ipa": "application/octet-stream",
        ".plist": "text/xml",
    }

    def end_headers(self) -> None:
        self.send_header("Cache-Control", "no-store")
        super().end_headers()


def serve_release(args: argparse.Namespace) -> None:
    directory = Path(args.directory).expanduser().resolve()
    required = ["index.html", "manifest.plist", "icon-57.png", "icon-512.png"]
    missing = [name for name in required if not (directory / name).is_file()]
    if missing or len(list(directory.glob("*.ipa"))) != 1:
        raise ValueError(f"release directory is incomplete; missing: {', '.join(missing) or 'IPA'}")

    handler = lambda *handler_args, **handler_kwargs: OTARequestHandler(  # noqa: E731
        *handler_args,
        directory=str(directory),
        **handler_kwargs,
    )
    server = ThreadingHTTPServer(("127.0.0.1", args.port), handler)
    print(f"Serving {directory} on http://127.0.0.1:{args.port}", flush=True)
    server.serve_forever()


def parser() -> argparse.ArgumentParser:
    root = argparse.ArgumentParser(description=__doc__)
    commands = root.add_subparsers(dest="command", required=True)

    prepare = commands.add_parser("export-options", help="write Xcode OTA export options")
    prepare.add_argument("--base-url", required=True)
    prepare.add_argument("--ipa-name", required=True)
    prepare.add_argument("--bundle-id", required=True)
    prepare.add_argument("--team-id", required=True)
    prepare.add_argument(
        "--method",
        choices=("debugging", "release-testing"),
        default="debugging",
        help="Xcode export method; use release-testing for a registered-device Ad Hoc build",
    )
    prepare.add_argument("--output", required=True)
    prepare.set_defaults(action=write_export_options)

    finish = commands.add_parser("finish", help="validate an export and add its install page")
    finish.add_argument("--directory", required=True)
    finish.add_argument("--base-url", required=True)
    finish.set_defaults(action=finish_release)

    serve = commands.add_parser("serve", help="serve one completed release on loopback")
    serve.add_argument("--directory", required=True)
    serve.add_argument("--port", type=int, default=8477)
    serve.set_defaults(action=serve_release)
    return root


def main() -> None:
    args = parser().parse_args()
    try:
        args.action(args)
    except (OSError, ValueError, plistlib.InvalidFileException, zipfile.BadZipFile) as error:
        raise SystemExit(f"error: {error}") from error


if __name__ == "__main__":
    main()
