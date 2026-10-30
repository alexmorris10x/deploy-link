#!/usr/bin/env python3
"""Fixture tests for ota_queue.py: publish, prune, the JSON feed, manifests and download receipts."""

from __future__ import annotations

import argparse
import contextlib
import io
import json
import os
import plistlib
import sys
import tempfile
import threading
import unittest
import urllib.request
import zipfile
from email.message import Message
from http.server import ThreadingHTTPServer
from pathlib import Path
from urllib.parse import unquote
from unittest import mock

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

    def test_expired_profiles_leave_the_queue(self) -> None:
        record = self.queue.publish(make_app(self.work, "5"))
        path = self.queue.root / record["id"] / "release.json"
        data = json.loads(path.read_text())
        data["profileExpires"] = "2026-01-01T00:00:00Z"
        path.write_text(json.dumps(data))
        self.assertEqual(self.queue.prune_expired(), [record["id"]])
        self.assertEqual(self.queue.records(), [])

    def test_prune_keeps_newest_of_every_app(self) -> None:
        for build in ("1", "2"):
            record = self.queue.publish(make_app(self.work, build))
            os.rename(self.queue.root / record["id"], self.queue.root / f"2026010{build}T000000Z-{record['id'][-8:]}")
        for bundle in {r["bundleIdentifier"] for r in self.queue.records()}:
            self.queue.prune(bundle, 1)
        self.assertEqual([r["build"] for r in self.queue.records()], ["2"])

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
        self.original_log_request = self.server.RequestHandlerClass.log_request
        self.server.RequestHandlerClass.log_request = lambda *args: None
        self.credential = ota_queue.ManagementCredential(self.queue.root)
        self.management_token = self.credential.read()
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

    def test_tagged_install_is_remembered_per_device(self) -> None:
        tag = "?device=ABC-123&family=iPad"
        item = plistlib.loads(self.get(f"/r/{self.record['id']}/manifest.plist{tag}")[2])["items"][0]
        ipa_url = {asset["kind"]: asset["url"] for asset in item["assets"]}["software-package"]
        self.assertTrue(ipa_url.endswith("app.ipa?device=ABC-123&family=iPad"))
        self.get(f"/r/{self.record['id']}/app.ipa{tag}")
        feed = json.loads(self.get("/api/releases?device=ABC-123&family=iPad")[2])
        installed = feed["installed"]["com.example.sample"]
        self.assertEqual((installed["build"], installed["available"]), ("7", True))
        self.assertEqual(json.loads(self.get("/api/releases?device=OTHER&family=iPhone")[2])["installed"], {})
        # The memory outlives the release.
        self.queue.remove(self.record["id"])
        installed = json.loads(self.get("/api/releases?device=ABC-123")[2])["installed"]["com.example.sample"]
        self.assertFalse(installed["available"])

    def test_untagged_history_falls_back_to_family(self) -> None:
        iphone = "com.apple.appstored/1.0 iOS/27.0.1 model/iPhone15,3 hwp/t8120"
        self.queue.record_download(self.record["id"], {"at": "2026-10-19T07:10:00Z", "complete": True, "userAgent": iphone})
        installed = json.loads(self.get("/api/releases?device=NEW-ID&family=iPhone")[2])["installed"]
        self.assertEqual(installed["com.example.sample"]["build"], "7")

    def test_active_download_is_listed(self) -> None:
        token = object()
        self.queue.start_download(token, self.record["id"], "ABC", "iPhone", 100)
        self.queue.progress(token, 40)
        active = json.loads(self.get("/api/releases")[2])["releases"][0]["activeDownloads"]
        self.assertEqual(active[0]["deviceId"], "ABC")
        self.assertEqual((active[0]["bytes"], active[0]["totalBytes"]), (40, 100))
        self.queue.finish_download(token)
        self.assertEqual(json.loads(self.get("/api/releases")[2])["releases"][0]["activeDownloads"], [])

    def test_delete_removes_release(self) -> None:
        request = urllib.request.Request(f"{self.local}/api/releases/{self.record['id']}", method="DELETE",
                                         headers={"Authorization": "Bearer " + self.management_token})
        with urllib.request.urlopen(request) as response:
            self.assertEqual(response.status, 200)
        self.assertEqual(json.loads(self.get("/api/releases")[2])["releases"], [])
        with self.assertRaises(urllib.error.HTTPError) as caught:
            urllib.request.urlopen(request)
        self.assertEqual(caught.exception.code, 404)

    def test_delete_requires_management_capability(self) -> None:
        target = f"{self.local}/api/releases/{self.record['id']}"
        for headers, suffix in (({}, ""), ({"Authorization": "Bearer " + "0" * 64}, ""),
                                ({}, "?token=" + self.management_token)):
            request = urllib.request.Request(target + suffix, method="DELETE", headers=headers)
            with self.assertRaises(urllib.error.HTTPError) as caught:
                urllib.request.urlopen(request)
            self.assertEqual(caught.exception.code, 401)
            self.assertIn("Bearer", caught.exception.headers["WWW-Authenticate"])
            self.assertTrue(self.queue.release_dir(self.record["id"]).is_dir())

    def test_management_validation_and_rotation(self) -> None:
        def request(token):
            return urllib.request.Request(self.local + "/api/management", headers={"Authorization": "Bearer " + token})
        with self.assertRaises(urllib.error.HTTPError) as caught:
            self.get("/api/management")
        self.assertEqual(caught.exception.code, 401)
        with urllib.request.urlopen(request(self.management_token)) as response:
            self.assertEqual(json.load(response), {"canDelete": True})
        self.credential.rotate()
        with self.assertRaises(urllib.error.HTTPError) as caught:
            urllib.request.urlopen(request(self.management_token))
        self.assertEqual(caught.exception.code, 401)
        with urllib.request.urlopen(request(self.credential.read())) as response:
            self.assertEqual(response.status, 200)

    def test_management_credential_is_never_served_or_logged(self) -> None:
        self.server.RequestHandlerClass.log_request = self.original_log_request
        output = io.StringIO()
        with contextlib.redirect_stderr(output):
            self.get("/api/releases?token=" + self.management_token)
            for path in ("/.management-token", "/r/" + self.record["id"] + "/.management-token"):
                with self.assertRaises(urllib.error.HTTPError) as caught:
                    self.get(path)
                self.assertEqual(caught.exception.code, 404)
        self.assertTrue(self.management_token not in output.getvalue(), "HTTP logs disclosed a management credential")
        self.assertNotIn("token=", output.getvalue())
        self.assertTrue(self.management_token.encode() not in self.get("/api/releases")[2], "The feed disclosed a management credential")

    def test_html_page_and_unknown_paths(self) -> None:
        _, content_type, body = self.get("/")
        self.assertTrue(content_type.startswith("text/html"))
        self.assertIn(b"Sample Dev", body)
        self.assertIn(b"itms-services://", body)
        for path in ("/r/../release.json", f"/r/{self.record['id']}/release.json", "/r/nope/app.ipa"):
            with self.assertRaises(urllib.error.HTTPError) as caught:
                self.get(path)
            self.assertEqual(caught.exception.code, 404)


class ManagementCredentialTests(unittest.TestCase):
    def setUp(self) -> None:
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.work = Path(temporary.name)
        self.credential = ota_queue.ManagementCredential(self.work / "queue")

    @staticmethod
    def headers(token: str) -> Message:
        headers = Message()
        headers["Authorization"] = "Bearer " + token
        return headers

    def test_secret_creation_permissions_and_preservation(self) -> None:
        self.credential.ensure()
        token = self.credential.read()
        self.assertTrue(ota_queue.MANAGEMENT_TOKEN.fullmatch(token) is not None, "Invalid credential format")
        self.assertEqual(self.credential.path.stat().st_mode & 0o777, 0o600)
        self.assertEqual(self.credential.path.parent.stat().st_mode & 0o777, 0o700)
        self.credential.ensure()
        self.assertTrue(self.credential.read() == token, "Repeated setup rotated the credential")
        with mock.patch.object(ota_queue.hmac, "compare_digest", wraps=ota_queue.hmac.compare_digest) as comparison:
            self.assertTrue(self.credential.authorizes(self.headers(token)))
            self.assertEqual(comparison.call_count, 1)

    def test_invalid_permissions_and_duplicate_headers_fail_closed(self) -> None:
        self.credential.ensure()
        headers = self.headers(self.credential.read())
        headers["Authorization"] = headers["Authorization"]
        self.assertFalse(self.credential.authorizes(headers))
        self.credential.path.chmod(0o644)
        with self.assertRaises(ValueError):
            self.credential.ensure()
        self.assertFalse(self.credential.authorizes(self.headers("0" * 64)))

    def test_symlinked_token_or_queue_is_refused(self) -> None:
        target = self.work / "elsewhere"
        target.write_text("unrelated content")
        self.credential.path.parent.mkdir()
        self.credential.path.symlink_to(target)
        with self.assertRaises((OSError, ValueError)):
            self.credential.ensure()
        self.assertEqual(target.read_text(), "unrelated content")
        linked_root = self.work / "linked-queue"
        linked_root.symlink_to(self.credential.path.parent)
        with self.assertRaises(ValueError):
            ota_queue.ManagementCredential(linked_root).ensure()

    def test_admin_copy_never_prints_the_secret(self) -> None:
        args = argparse.Namespace(root=self.credential.path.parent, rotate=False, copy=True)
        output = io.StringIO()
        with contextlib.redirect_stdout(output), mock.patch.object(ota_queue.sys, "platform", "darwin"), \
                mock.patch.object(ota_queue.subprocess, "run") as clipboard:
            ota_queue.command_admin_token(args)
        token = self.credential.read()
        self.assertTrue(token not in output.getvalue(), "The admin command disclosed the token")
        self.assertEqual(clipboard.call_count, 1)
        self.assertEqual(clipboard.call_args.args[0], ["/usr/bin/pbcopy"])
        self.assertTrue(clipboard.call_args.kwargs["input"] == token.encode("ascii"), "Clipboard received the wrong credential")


class ServeRouteTests(unittest.TestCase):
    def test_existing_route_is_preserved_and_unrelated_routes_are_allowed(self) -> None:
        config = {"TCP": {"443": {"HTTPS": True}}, "Web": {"mac.example.ts.net:443": {"Handlers": {
            "/": {"Proxy": "http://127.0.0.1:8477"}, "/other": {"Proxy": "http://127.0.0.1:8123"},
        }}}}
        original = json.dumps(config, sort_keys=True)
        self.assertEqual(ota_queue.serve_route_state(config, BASE_URL, 8477), "existing")
        self.assertEqual(json.dumps(config, sort_keys=True), original)
        del config["Web"]["mac.example.ts.net:443"]["Handlers"]["/"]
        self.assertEqual(ota_queue.serve_route_state(config, BASE_URL, 8477), "configure")
        self.assertEqual(ota_queue.serve_route_state({}, BASE_URL, 8477), "configure")

    def test_conflicting_root_funnel_tcp_and_api_routes_are_refused(self) -> None:
        configs = [
            {"Web": {"mac.example.ts.net:443": {"Handlers": {"/": {"Proxy": "http://127.0.0.1:9999"}}}}},
            {"AllowFunnel": {"mac.example.ts.net:443": True}},
            {"TCP": {"443": {"TCPForward": "127.0.0.1:9000"}}},
            {"Web": {"mac.example.ts.net:443": {"Handlers": {"/api/": {"Proxy": "http://127.0.0.1:9000"}}}}},
        ]
        for config in configs:
            with self.assertRaises(ValueError):
                ota_queue.serve_route_state(config, BASE_URL, 8477)

    def test_base_url_rejects_embedded_credentials_and_paths(self) -> None:
        for value in ("http://mac.example.ts.net", "https://name:password@mac.example.ts.net/",
                      "https://mac.example.ts.net/?token=example", "https://mac.example.ts.net/queue/"):
            with self.assertRaises(ValueError):
                ota_queue.normalized_base_url(value)


if __name__ == "__main__":
    unittest.main()
