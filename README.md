# Deploy Link

A private queue for your signed iOS development builds. Add a build in the Mac app or from the command line, then choose it on your iPhone or iPad and confirm the iOS installation prompt. The native Mac companion, Python server, and iOS client are free for the initial launch, with original source under [Apache 2.0](LICENSE).

- [Setup](docs/site/setup.html), [support](docs/site/support.html), [privacy](docs/site/privacy.html)
- [Product specification](docs/SPEC.md) and [remaining launch work](docs/TODO.md)
- [Distribution](docs/release/distribution.md), [App Store metadata](docs/release/app-store-metadata.md), and [App Review question](docs/release/app-review-question.md)

The native App Store release is being prepared. Apple's acceptance of the development-build installation workflow remains unresolved; an App Store record is not approval or a public download. The server also has a browser queue page.

## How it works

```text
signed .app or .ipa -> private queue on your Mac
                              |
              loopback server -> Tailscale Serve HTTPS
                              |
              iPhone/iPad -> choose build -> iOS install prompt
```

The server packages a signed device app, reads its bundle/version/profile metadata, and serves its manifest and IPA. The iOS client shows the queue and opens an `itms-services` URL when you choose Install. iOS downloads and validates the package; Deploy Link neither signs apps nor bypasses provisioning.

**Downloaded means the server completed the file transfer. It does not prove installation succeeded.** Open the target app to confirm it installed. The client can identify its own running version, but it cannot inspect other installed apps or learn about builds delivered outside this queue.

## Set up your Mac

[Download Deploy Link for Mac 1.0.1](https://github.com/alexmorris10x/deploy-link/releases/download/v1.0.1-mac/Deploy-Link-Mac-1.0.1-2.dmg) — signed and notarized, for macOS 14 or later on Apple silicon and Intel. [Release notes and checksum](https://github.com/alexmorris10x/deploy-link/releases/tag/v1.0.1-mac). The source installer remains available below.

Install full Xcode with its command-line tools selected in Xcode’s Locations settings, Python 3.9 or newer, and Tailscale on the Mac and iPhone/iPad. These prerequisites are separate from Deploy Link. The standalone Command Line Tools package is insufficient. You also need a valid signed iOS build for the intended devices/distribution method. The Mac must remain awake and signed in while serving. Tailscale has its own [plans](https://tailscale.com/pricing) and [terms](https://tailscale.com/terms).

### Native Mac companion

Open the downloaded disk image, drag **Deploy Link** into **Applications**, and open it. Use the setup control to install the bundled server. The app keeps a menu-bar item with server status, start/stop controls, queue opening and address copying, build publishing, and launch-at-login settings. Closing its window leaves the menu-bar item available.

Use **Add Build** to choose a signed device `.app` or `.ipa`, add a note, and publish it to your queue. Open or copy the queue address to connect your iPhone or iPad. The Mac app uses the same server, queue, and management token as the command-line setup. An existing installation is detected; updating the server preserves its queue and token. Quitting the Mac app does not erase builds or stop the separate server service.

### Command-line installation

Clone this repository or extract its source ZIP. Read the installer, then run it locally:

```sh
git clone https://github.com/alexmorris10x/deploy-link.git
cd deploy-link
bash scripts/install_ota_queue.sh
```

The installer sets up `com.10x.deploy-link-ota` as a login LaunchAgent and connects private Tailscale HTTPS port 8443 to loopback port 8477. It checks existing Serve/Funnel configuration rather than resetting unrelated services. Complete any Tailscale HTTPS setup it requests, then rerun. **Do not use Funnel or expose the queue publicly.** Reading and downloads are protected by your Tailscale/network rules, not by separate user accounts.

Default locations:

| Item | Path |
| --- | --- |
| Installed command | `~/.local/share/deploy-link/bin/ota-queue` |
| Builds and transfer history | `~/.local/share/ios-ota/queue/` |
| Management credential | `~/.local/share/ios-ota/queue/.management-token` |
| Runtime log | `~/.local/share/deploy-link/logs/ota-queue.log` |
| Login agent | `~/Library/LaunchAgents/com.10x.deploy-link-ota.plist` |

The installer prints your own HTTPS queue address. Open it in Safari on your Tailscale-connected device, or enter it in the iOS app's Settings. There is no personal/default server bundled into the app. It accepts an HTTPS host root without credentials, query, or fragment.

## Publish and manage builds

```sh
"$HOME/.local/share/deploy-link/bin/ota-queue" publish "/path/to/YourApp.app" --note "What changed"
"$HOME/.local/share/deploy-link/bin/ota-queue" list
"$HOME/.local/share/deploy-link/bin/ota-queue" devices
```

A signed `.ipa` can replace the `.app` argument. Simulator builds and unsigned packages are not installable. Apple can reject installation because of profile expiry, device registration, certificate status, or OS compatibility even after a transfer succeeds.

The queue groups builds by app, newest first. The client shows notes, transfer progress, and completed-download history, asks before repeating or downgrading a download, and avoids overlapping downloads of the same app on one device.

Deleting from iOS needs an optional management token. Copy it locally without printing it:

```sh
"$HOME/.local/share/deploy-link/bin/ota-queue" admin-token --copy
```

Paste it into **Management token (optional)** in Settings through a private transfer method. It stays in the device's Keychain for that server. Browsing and downloading do not need it. `admin-token --rotate --copy` replaces the server token and revokes existing management access. Never put a token in a URL, issue, screenshot, or shared log.

Queue removal affects every device's ability to download that package; it does not uninstall the app from devices:

```sh
"$HOME/.local/share/deploy-link/bin/ota-queue" remove <release-id>
"$HOME/.local/share/deploy-link/bin/ota-queue" prune --keep 1
```

Publishing retains the newest release per app by default (`--keep N` changes that). Expired releases are removed on publish and queue refresh. A separate `devices.json` retains each device's last completed downloads after releases leave the queue. The public installer does not add a nightly cleanup schedule. Keep original builds elsewhere if you need an archive.

## Update, build, and contribute

For the native app, replace the application with a newer downloaded version. The separately installed server is preserved; to update it, get the current source and rerun the installer. This preserves the queue and token. Custom-path and uninstall instructions are in [distribution](docs/release/distribution.md) and [setup](docs/site/setup.html).

The macOS app is in `mac/`. Run `xcodegen generate` there and build the **DeployLinkMac** scheme. The release product is **Deploy Link.app** (`com.10x.deploylink.mac`). [The distribution guide](docs/release/distribution.md) covers the Developer ID signing and notarization script; building the package does not install or start the server.

The iOS app is in `companion/` and uses XcodeGen. Run `xcodegen generate` there. The **DeployLinkCompanion** scheme builds **Deploy Link Dev** (`com.10x.deploylink.dev`) in Debug and **Deploy Link** (`com.10x.deploylink`) in Release. Use your own authorized signing team; the maintainer's checked-in team does not grant signing rights. Maintainer builds use `~/bin/ios-dev-build`.

The launch change's 25 Python server tests passed, and a signed iOS device build passed. Those checks do not establish installation acceptance for another developer's provisioning or App Store approval. See [TODO](docs/TODO.md) for the remaining customer setup and release inputs. Run `python3 ota/test_ota_queue.py` when changing the server.

No app build, private provisioning file, management token, or personal server address belongs in a public issue. Both source components are inspectable; that is not a security-audit claim.
