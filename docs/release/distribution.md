# Launch distribution

Deploy Link is initially free: the native Mac companion, Python server, and iOS client have no subscription or in-app purchase. Original source is Apache 2.0. The Mac companion is distributed directly with Developer ID signing and Apple notarization; the source installer remains available. App Store policy for the iOS install action remains an open launch input; [the exact question and support-case status](app-review-question.md) are tracked separately.

## Public site and source

The site lives in `docs/site/`, with local assets and no build dependencies. `.github/workflows/pages.yml` publishes that folder to `https://alexmorris10x.github.io/deploy-link/` using official Pages actions and runs only when the repository is public. No paid hosting or domain is required.

The page intentionally has no App Store badge before publication. Add the real listing link only after the app is available. The signed, notarized Mac 1.0 download is published on GitHub Releases and accessible without signing in. The source-installation instructions locally run `bash scripts/install_ota_queue.sh`; they do not download and execute an unseen script through a shell pipe.

## Native Mac release

Project: `mac/project.yml`; generated project: `mac/DeployLinkMac.xcodeproj`; scheme: `DeployLinkMac`; product: `Deploy Link.app`; bundle: `com.10x.deploylink.mac`. The app requires macOS 14 or later. Release builds contain both `arm64` and `x86_64` architectures.

The companion bundles the queue server and installer. Xcode, Python 3.9+, and Tailscale are installed separately. Opening the app shows server status and setup; its menu-bar item provides queue opening/address copying, build publishing, server start/stop, and app launch-at-login controls. Closing the app window leaves the menu-bar item available. The server is a separate login LaunchAgent, so quitting the companion does not stop it. Replacing the Mac app preserves the existing queue and management token. To update the separately installed server, update the source and rerun its installer; the first release does not automatically replace a running server.

Run the packager from the worktree on the Mac. These values are identity/profile names, not passwords:

```sh
export DEPLOY_LINK_DEVELOPER_ID='Developer ID Application: Alex Morris (98CDQPW36W)'
export DEPLOY_LINK_NOTARY_PROFILE='deploy-link-notary'
scripts/package-mac-release.sh --check
scripts/package-mac-release.sh
```

The maintainer’s Keychain profile uses the existing authorized App Store Connect key. Other developers use their own Developer ID identity and notarization profile. Do not print credentials or put them in the repository. `--check` validates local tools, the signing identity, and project presence; it does not contact the notarization service or authenticate that profile.

The normal run checks Mac capacity, builds through `~/bin/ios-dev-build`, and stages everything under `~/Developer/builds/agent-scratch/deploy-link-mac-release/package.*`. `DEPLOY_LINK_RELEASE_ROOT` can choose another task folder under `~/Developer/builds`. The script signs nested code with hardened runtime, checks the bundle identifier and both architectures, verifies the absence of debug entitlements, notarizes and staples the app, then creates, signs, notarizes, staples, and verifies the DMG. The disk image contains the app, an Applications shortcut, `LICENSE`, and `NOTICE`. It emits the DMG and SHA-256 file paths.

Packaging never launches the app, runs its installer, alters the live queue/service, publishes to GitHub, or submits to App Store Connect. The upload to Apple is notarization only.

Version 1.0, build 1 is published as `Deploy-Link-Mac-1.0-1.dmg` on [release `v1.0.0-mac`](https://github.com/alexmorris10x/deploy-link/releases/tag/v1.0.0-mac), from source commit `800e8ab`. Public download:

```text
https://github.com/alexmorris10x/deploy-link/releases/download/v1.0.0-mac/Deploy-Link-Mac-1.0-1.dmg
```

The corresponding `.sha256` file is published beside it. Both app and DMG notarization were accepted, both tickets were stapled and validated, and signature, image-integrity, and Gatekeeper checks passed. An anonymous full download matched SHA-256 `e27ebda0a15b412e4d95ee364ba5f697d9df4dadbe6507d4f42ce739d51ea471`. Customers and reviewers use the same public download. For future releases, update public download links only after the asset is available without authentication.

## Runtime contract

- Python 3.9 or newer on macOS; Tailscale installed and connected. Installer: `bash scripts/install_ota_queue.sh`.
- Default runtime: `~/.local/share/deploy-link/`; default queue: `~/.local/share/ios-ota/queue/`; LaunchAgent: `com.10x.deploy-link-ota`.
- Default loopback port: 8477, reached through the user's private Tailscale HTTPS address. Do not use Funnel or expose the queue publicly.
- Queue listing and downloads use Tailscale/network access rules. `DELETE /api/releases/{id}` and `GET /api/management` require `Authorization: Bearer <management-token>`. Tokens do not belong in query strings or logs.
- The server stores its token at `<queue-root>/.management-token` with owner-only permissions. `ota-queue admin-token --copy` copies it locally without printing it. For a custom queue: `ota-queue --root <queue-root> admin-token --copy`.
- Installer overrides, when needed: `DEPLOY_LINK_RUNTIME_ROOT`, `DEPLOY_LINK_QUEUE_ROOT`, `DEPLOY_LINK_OTA_PORT`, `DEPLOY_LINK_OTA_BASE_URL`, `DEPLOY_LINK_PYTHON`, and `DEPLOY_LINK_TAILSCALE`. A custom base URL must be an HTTPS host root with no credentials, query, or fragment.
- Publishing retains the newest build per app by default (`--keep N` retains more); pruning and expired-profile removal can delete queued packages. The public installer does not add a nightly pruning schedule. Do not copy a personal machine's cleanup schedule into the public setup requirements.

The completed-download record is not installation proof. Validate a real installation by opening the target app, and keep that fact separate from successful queue listing or file transfer.

## License inventory

| Component | Treatment |
| --- | --- |
| Original macOS/iOS Swift apps, Python server, scripts, docs, icon | Apache 2.0; preserve `LICENSE` and `NOTICE`. |
| Apple frameworks | Supplied by Apple SDKs/OS; no framework source or binaries vendored here. |
| Python and its standard library | Installed separately; no pip packages or bundled Python runtime. |
| Tailscale | Installed separately under its own terms; no binary or SDK included. |
| XcodeGen / GitHub Pages actions | External build/deploy tools, not app runtime dependencies. |

The checked source has no Swift Package, CocoaPods, Carthage, or pip runtime dependency and no conflicting third-party notice was found. The owner must still have publication rights to the original source/artwork and prepare repository history for public visibility. This is a license inventory, not a security audit.

Use [the metadata draft](app-store-metadata.md) for the listing and review-access inputs. Signing, App Review access, and submission remain separate from publishing the open-source server and website.
