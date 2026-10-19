#!/usr/bin/env python3
"""Fixture tests for ota_queue.py: publish, prune, the JSON feed, manifests and download receipts."""

from __future__ import annotations

import json
import os
import plistlib
import sys
import tempfile
import threading
import unittest
import urllib.request
import zipfile
from http.server import ThreadingHTTPServer
from pathlib import Path
from urllib.parse import unquote

sys.path.insert(0, str(Path(__file__).resolve().parent))
import ota_queue  # noqa: E402

BASE_URL = "https://mac.example.ts.net/"


def make_app(parent: Path, build: str, name: str = "Sample.app") -> Path:
    app = parent / build / name
    (app / "Frameworks" / "Kit.framework").mkdir(parents=True)
    info = {
        "CFBundleIdentifier": "com.example.sample",
        "CFBundleShortVersionString": "1.2",
        "CFBundleVersion": build,
        "CFBundleName": "Sample",
        "CFBundleDisplayName": "Sample Dev",
        "CFBundleIcons": {"CFBundlePrimaryIcon": {"CFBundleIconFiles": ["AppIcon60x60"]}},
    }
    (app / "Info.plist").write_bytes(plistlib.dumps(info))
    profile = plistlib.dumps({
        "Name": "iOS Team Provisioning Profile",
        "Entitlements": {"get-task-allow": True},
        "ProvisionedDevices": ["device-a", "device-b"],
    })
    (app / "embedded.mobileprovision").write_bytes(b"\x30\x80CMS-envelope" + profile + b"\x00signature")
    (app / "AppIcon60x60@2x.png").write_bytes(b"small-icon")
    (app / "AppIcon60x60@3x.png").write_bytes(b"large-icon")
    executable = app / "Sample"
    executable.write_bytes(b"binary")
    executable.chmod(0o755)
    (app / "Frameworks" / "Kit.framework" / "Kit").write_bytes(b"framework")
    os.symlink("Kit.framework/Kit", app / "Frameworks" / "Kit-link")
    return app


class QueueTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.work = Path(self.temporary.name)
        self.queue = ota_queue.Queue(self.work / "queue")

    def test_publish_packages_app_and_reads_identity(self) -> None:
        record = self.queue.publish(make_app(self.work, "100"), note="first")
        self.assertRegex(record["id"], ota_queue.RELEASE_ID)
        self.assertEqual(record["title"], "Sample Dev")
        self.assertEqual(record["build"], "100")
        self.assertEqual(record["profileKind"], "development")
        self.assertEqual(record["provisionedDeviceCount"], 2)
        self.assertTrue(record["hasIcon"])
        directory = self.queue.root / record["id"]
        self.assertEqual((directory / "icon.png").read_bytes(), b"large-icon")
        with zipfile.ZipFile(directory / "app.ipa") as archive:
            names = archive.namelist()
            self.assertIn("Payload/Sample.app/Info.plist", names)
            executable = archive.getinfo("Payload/Sample.app/Sample")
            self.assertTrue((executable.external_attr >> 16) & 0o100)
            link = archive.getinfo("Payload/Sample.app/Frameworks/Kit-link")
            self.assertEqual((link.external_attr >> 16) & 0o170000, 0o120000)
            self.assertEqual(archive.read(link), b"Kit.framework/Kit")

    def test_publish_rejects_unsigned_app(self) -> None:
        app = make_app(self.work, "1")
        (app / "embedded.mobileprovision").unlink()
        with self.assertRaises(ValueError):
            self.queue.publish(app)
        self.assertEqual(self.queue.records(), [])
        self.assertEqual([p.name for p in self.queue.root.iterdir()], [])

    def test_publish_keeps_newest_releases_per_app(self) -> None:
        ids = []
        for build in ("1", "2", "3"):
            record = self.queue.publish(make_app(self.work, build), keep=2)
            ids.append(record["id"])
            # Release ids are second-resolution; keep them distinct and ordered.
            os.rename(self.queue.root / record["id"], self.queue.root / f"2026010{build}T000000Z-{record['id'][-8:]}")
        builds = [record["build"] for record in self.queue.records()]
        self.assertEqual(builds, ["3", "2"])

    def test_release_ids_cannot_escape_the_queue(self) -> None:
        with self.assertRaises(KeyError):
            self.queue.release_dir("../etc")


class ServerTests(unittest.TestCase):
    def setUp(self) -> None:
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        work = Path(temporary.name)
        self.queue = ota_queue.Queue(work / "queue")
        self.record = self.queue.publish(make_app(work, "7"), note="try me")
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), ota_queue.make_handler(self.queue, BASE_URL))
        self.server.RequestHandlerClass.log_message = lambda *args: None
        thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)
        self.local = f"http://127.0.0.1:{self.server.server_address[1]}"

    def get(self, path: str) -> tuple:
        with urllib.request.urlopen(self.local + path) as response:
            return response.status, response.headers.get("Content-Type"), response.read()

    def test_feed_lists_release_with_install_link(self) -> None:
        status, content_type, body = self.get("/api/releases")
        self.assertEqual(status, 200)
        self.assertEqual(content_type, "application/json")
        releases = json.loads(body)["releases"]
        self.assertEqual(len(releases), 1)
        release = releases[0]
        self.assertEqual(release["build"], "7")
        self.assertEqual(release["note"], "try me")
        self.assertEqual(release["downloadCount"], 0)
        manifest_url = f"{BASE_URL}r/{self.record['id']}/manifest.plist"
        self.assertEqual(release["manifestURL"], manifest_url)
        prefix = "itms-services://?action=download-manifest&url="
        self.assertTrue(release["installURL"].startswith(prefix))
        self.assertEqual(unquote(release["installURL"][len(prefix):]), manifest_url)

    def test_manifest_points_at_tailnet_ipa(self) -> None:
        _, content_type, body = self.get(f"/r/{self.record['id']}/manifest.plist")
        self.assertEqual(content_type, "text/xml")
        item = plistlib.loads(body)["items"][0]
        self.assertEqual(item["metadata"]["bundle-identifier"], "com.example.sample")
        self.assertEqual(item["metadata"]["title"], "Sample Dev")
        urls = {asset["kind"]: asset["url"] for asset in item["assets"]}
        self.assertEqual(urls["software-package"], f"{BASE_URL}r/{self.record['id']}/app.ipa")
        self.assertIn("display-image", urls)

    def test_full_download_is_recorded(self) -> None:
        _, _, body = self.get(f"/r/{self.record['id']}/app.ipa")
        self.assertEqual(len(body), self.record["ipaBytes"])
        release = json.loads(self.get("/api/releases")[2])["releases"][0]
        self.assertEqual(release["downloadCount"], 1)
        self.assertIsNotNone(release["lastDownloadedAt"])
        self.assertEqual(release["downloadedBy"][0]["device"], "Unknown device")

    def test_downloads_are_grouped_by_device(self) -> None:
        iphone = "com.apple.appstored/1.0 iOS/27.0.1 model/iPhone15,3 hwp/t8120"
        ipad = "com.apple.appstored/1.0 iOS/27.0.1 model/iPad14,1 hwp/t8110"
        for at, agent in (("2026-10-19T07:00:00Z", iphone), ("2026-10-19T07:05:00Z", ipad), ("2026-10-19T07:10:00Z", iphone)):
            self.queue.record_download(self.record["id"], {"at": at, "complete": True, "userAgent": agent})
        self.queue.record_download(self.record["id"], {"at": "2026-10-19T07:20:00Z", "complete": False, "userAgent": ipad})
        release = json.loads(self.get("/api/releases")[2])["releases"][0]
        self.assertEqual(release["downloadedBy"], [
            {"device": "iPhone", "at": "2026-10-19T07:10:00Z"},
            {"device": "iPad", "at": "2026-10-19T07:05:00Z"},
        ])

    def test_html_page_and_unknown_paths(self) -> None:
        _, content_type, body = self.get("/")
        self.assertTrue(content_type.startswith("text/html"))
        self.assertIn(b"Sample Dev", body)
        self.assertIn(b"itms-services://", body)
        for path in ("/r/../release.json", f"/r/{self.record['id']}/release.json", "/r/nope/app.ipa"):
            with self.assertRaises(urllib.error.HTTPError) as caught:
                self.get(path)
            self.assertEqual(caught.exception.code, 404)


if __name__ == "__main__":
    unittest.main()
