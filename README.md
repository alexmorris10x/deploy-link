# Deploy Link

Delivers Alex's signed iOS development builds from the Mac to his iPhone and
iPad, over Wi-Fi or cellular, through install links. It is the only device
delivery route since 21 October 2026; the older warm-session direct install
(RemotePairing over Tailscale, the macOS status app and the probe app) was
removed and lives on in the `archive/direct-install` tag.

```text
signed .app -> ota-queue publish -> ~/.local/share/ios-ota/queue/<id>/   (on the Mac)
                                          |
        loopback server :8477 <- tailscale serve (tailnet-only HTTPS)
                                          |
          Deploy Link app on iPhone/iPad -> Install -> iOS prompt -> download + install
```

## How a link works

- `ota-queue publish` zips the signed `.app` into an IPA, reads its bundle ID,
  version, build, icon and provisioning profile, and stores it on the Mac in
  `~/.local/share/ios-ota/queue/<release id>/` (`app.ipa`, `icon.png`,
  `release.json`, `downloads.jsonl`). Nothing is uploaded anywhere else.
- The `com.10x.deploy-link-ota` LaunchAgent serves that folder on
  `127.0.0.1:8477`; `tailscale serve` publishes it at the Mac's private
  tailnet HTTPS address. Only devices on Alex's tailnet can reach it, and only
  devices in the app's provisioning profile can install a build.
- Each release has a manifest (`/r/<id>/manifest.plist`). The app hands iOS an
  `itms-services://` link to it; iOS asks Alex to confirm, then downloads the
  IPA straight from the Mac and installs it. The Mac must be awake and online.
- The app tags its links with the device (`?device=<id>&family=iPhone`), so the
  Mac knows which download is running on which device and remembers, per
  device, the last build of each app that finished downloading
  (`queue/devices.json`). That memory outlives the release itself.

## What the app does

- Lists the queue grouped by app, newest build first, with older builds folded
  away, and says which build of each app is on this device.
- **Install / Update** when the build is new to the device; **Installed** (asks
  before installing it again) when the device already has that exact build; a
  plain **Install** that asks first when the build is older than the device's.
- While a build is downloading it shows the progress and blocks a second
  install of any build of that app on the same device. After tapping Install it
  waits up to a minute for the iOS prompt to be confirmed.
- Checks a release still exists before installing; one that left the queue
  drops off the list with a note. If the device's build has left the queue, the
  app says so.
- Swipe a build to remove it from the Mac's queue.

iOS gives no installation readback to an install link, so "on this device"
means the device finished downloading that build from Deploy Link. A build
installed some other way (Xcode, USB) isn't known.

## How builds are cleared out

- Publishing keeps the newest five releases of each app and deletes the rest.
- Every night at 03:50 the Mac's `mac-dev-cleanup` job runs `ota-queue prune`,
  which keeps only the newest build of each app.
- Releases whose provisioning profile has expired are deleted (on publish and
  whenever the app refreshes).
- `ota-queue remove <id>`, or a swipe in the app, deletes one release.

## Commands

```sh
ota=~/.local/share/deploy-link/bin/ota-queue
"$ota" publish /path/to/App.app --note "What changed"
"$ota" list      # newest first: app ID, build, note, downloads per device
"$ota" devices   # the last build of each app every device downloaded
"$ota" prune     # keep only the newest build of each app (--keep N)
"$ota" remove <release-id>
```

`scripts/install_ota_queue.sh` installs the server as `ota-queue` with its
LaunchAgent and makes sure `tailscale serve` proxies to port 8477. Rerun it
from the main checkout after changing `ota/ota_queue.py`. Run
`python3 ota/test_ota_queue.py` for the fixture tests.

## The app

[`companion/`](companion/) is the Deploy Link app (XcodeGen `project.yml`,
iPhone and iPad). Debug builds **Deploy Link Dev** (`com.10x.deploylink.dev`,
orange DEV banner icon in `AppIcon-Dev`), the build Alex keeps installed;
Release keeps `com.10x.deploylink`. The icon is drawn in
[`docs/AppIcon.svg`](docs/AppIcon.svg) (an arrow dropping into a phone);
render it with `rsvg-convert -w 1024 -h 1024 docs/AppIcon.svg | magick - -background black -alpha remove -alpha off PNG24:companion/DeployLinkCompanion/Assets.xcassets/AppIcon.appiconset/AppIcon.png`.
Regenerate the Dev icon with
`~/bin/ios-dev-icon --catalog companion/DeployLinkCompanion/Assets.xcassets/AppIcon.appiconset companion/DeployLinkCompanion/Assets.xcassets/AppIcon-Dev.appiconset`. Build it with the normal Debug device
build and publish it to the queue like any other app; it marks the build it is
running, so updating it through itself is the end-to-end test.

Do not use Tailscale Funnel for private development builds.
