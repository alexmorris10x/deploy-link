# Deploy Link

This repository tests direct iOS development installs when the Mac and a paired
iPhone or iPad are on different physical networks.

## Verified result

The fastest proven lane is a warm Apple RemotePairing session carried by
Tailscale for each device profile. Xcode still builds and signs the app. The
vendored Deploy Link daemon keeps the authenticated Apple session open while a
paired device remains on unrelated Wi-Fi or an iPhone moves from Wi-Fi to
cellular, then installs the signed `.app` through Apple's normal installation
services.

```text
Xcode signed .app
       |
       v
Deploy Link CLI -> owner-only local socket -> warm Apple tunnel
                                                |
                                                v
                                Tailscale -> iPhone or iPad
```

On August 31, 2026, this lane delivered the signed Audiobook Player Beta
artifact identified as version `1.1.8` build `20260831131723` to the paired iPad
Mini while the iPad used coffee-shop Wi-Fi and the Mac used a different
physical network. The full install of the 156,181,588-byte payload took 20
minutes 30.569 seconds. It continued beyond the old five-minute total deadline
because real transfer progress reset the rolling no-progress limit, then
reached Apple's `InstallComplete` and `DataComplete` states and passed
InstallationProxy bundle-identifier readback. This confirms the iPad profile
and unrelated-Wi-Fi installation lane. The exact on-device version and build,
and the app's runtime behavior, were not separately verified.

On August 30, 2026, this lane installed two signed Audiobook Player Beta builds
through the same warm session. Each app payload was about 149.8 MB. The second
install delivered version `1.1.8` build `20260830144800` while:

- Wi-Fi was off;
- no USB device was connected;
- Apple CoreDevice reported the iPhone as unavailable; and
- Tailscale reached the iPhone through its cellular address.

Both transfers reached payload completion, Apple's installer completed
`InstallComplete` and `DataComplete`, and InstallationProxy readback found the
installed bundle. The payload phases took about 140 and 168 seconds; the full
attempts took about 150 and 175 seconds. Alex then opened the second build and
confirmed its visible Version and Build rows in Settings.

The earlier small proof replaced Xcode Cellular Probe build 3 with build 4
under the same cellular conditions. After Wi-Fi returned, `devicectl` read back
`CFBundleVersion` 4 from that probe.

## Important boundary

This is warm-session continuity, not a pure-cellular cold start.

- Start each named Deploy Link profile while its device is on any Wi-Fi and
  Tailscale is connected.
- Wait until the bridge reports `active`.
- The device can then accept installs from unrelated Wi-Fi. A cellular-capable
  iPhone can also move to cellular and accept repeated direct installs.
- If the Apple session ends, the device must join any Wi-Fi so the daemon can
  acquire a new RemotePairing listener.

The device does not need to share the Mac's local network. This prototype does
not yet provide a native Xcode Run Destination, LLDB debugging, or live device
console access.

## Source ownership

The exact successful Deploy Link source is maintained under [`deploy-link/`](deploy-link/).
It is a downstream copy based on:

- source: `zjz-connect/xcode-deploy-link-deploy-beyond-local-network`;
- commit: `66a39ee4da4ac7f6ad26c871552120ad56ead92b`;
- version: `0.9.1`;
- Link Core: `danielpaulus/go-ios` at
  `3ebc297691a9e364772aef027744ebc0c49421a5`; and
- Link Core patch SHA-256:
  `4dc515286bf1fce9df7191daf9289c55e77e7c6d73b35c9388b5bc5237a6e5ed`.

The maintained downstream changes add the Tailnet connection path, bounded
privacy-filtered install diagnostics, and profile-scoped unattended install
jobs with durable local receipts. They do not add a remote control API.

See [`deploy-link/UPSTREAM.md`](deploy-link/UPSTREAM.md) and the included license
files. Profiles, pairing records, logs, binaries, toolchains, and build caches
must stay outside Git.

## Install the host tool

```sh
./scripts/install_deploy_link.sh
```

The installer also places a native macOS app at `/Applications/Deploy Link.app`.
Open it from Applications, Spotlight, or the menu bar to see each configured
device, start or stop its bridge, follow install progress, and drop a signed iOS
`.app` onto a selected device. The app hides its Dock icon by default; turn off
`Hide from Dock` in the App settings when a normal Dock presence is preferred.
Closing or quitting the macOS app leaves the independent device bridge services
running; stopping a bridge is always an explicit action. The app can optionally
install its own user LaunchAgent for Launch at Login. That login item starts only
the UI and does not control the device bridges.

The wrapper uses the vendored, pinned source and keeps generated state under:

```text
~/.local/share/deploy-link/
```

That directory is durable local tool state. Keep it while the bridge is in use.
Remove it only when intentionally uninstalling Deploy Link. It can contain a
private Apple pairing record and must never be committed or shared.

## Pair and configure each device

Create a RemotePairing record while the device is available locally. Follow the
[`pymobiledevice3` iOS 17+ tunnel guide](https://github.com/doronz88/pymobiledevice3/blob/master/docs/guides/ios17-tunnels.md).
On current iOS and iPadOS, the matching device screen is:

```text
Settings -> Privacy & Security -> Developer Mode -> Other Devices
```

Store the resulting record in an owner-only location, such as:

```text
~/.local/share/deploy-link/pairing/
```

Then create an owner-only profile. Use placeholders here; never put the real
profile or pairing record in this repository.

```sh
deploy_link="$HOME/.local/share/deploy-link/bin/deploy-link"
profile="$HOME/.local/share/deploy-link/profiles/iphone.json"

"$deploy_link" configure \
  --profile "$profile" \
  --device-label "development iphone" \
  --remote-identifier "REMOTE-PAIRING-IDENTIFIER" \
  --target-tailnet-ip "TAILSCALE-IP" \
  --pair-record "$HOME/.local/share/deploy-link/pairing/remote_DEVICE.plist"

"$deploy_link" doctor --profile "$profile"
```

Use a different `--local-forward-port` for every additional device. For
example, keep `61015` for the iPhone and use `61016` for the iPad.

## Start durable warm sessions

Keep each device on Wi-Fi or a hotspot, with Tailscale connected, for its first
acquisition. Install one isolated macOS service per profile:

```sh
"$deploy_link" launch-agent install --profile "$profile"
```

In another terminal:

```sh
"$deploy_link" status --profile "$profile"
```

Do not change the named device's network until the state is `active`. An iPhone
may then switch to cellular; an iPad can remain on unrelated Wi-Fi. The service
survives terminal and Codex exits, restarts after a process failure, and waits
instead of replacing a foreground daemon that already owns the profile. Each
device has a separate service, socket, local port, and log. On AC power, the
service asks macOS to prevent idle system sleep.

Deploy Link can relearn a changed RemotePairing port with a bounded,
protocol-verified scan. Failed scans back off to avoid continuous device load.
It cannot create a cold Apple listener. A Mac restart, closed laptop lid, iOS
restart, or ended Apple session can still require the device to join Wi-Fi or a
hotspot once before it becomes `active` again.

## Build and install remotely

Build one signed iOS or iPadOS app with the project's normal Xcode development
lane. After Deploy Link is active, keep the paired device on unrelated Wi-Fi or
move an iPhone to cellular, leave Tailscale connected, and install the signed
app output:

```sh
"$deploy_link" install \
  --profile "$profile" \
  /absolute/path/to/YourApp.app
```

Slow public networks can take longer than five minutes. The profile's
`install_timeout_seconds` is therefore a no-progress limit, not a total install
limit. Payload bytes, Apple installation stages, and device percentages reset
it. A separate hard safety limit is at least 30 minutes. If the service truly
stalls, Deploy Link closes only that install stream and preserves the warm outer
session. ZipConduit has no resume support, so a later retry starts from byte
zero. Deploy Link retries transient EOF, reset, broken-pipe, and network-timeout
failures within the same accepted install, for at most three total transfer
attempts. It retries only while the same warm session and generation remain
active; stalls, device rejections, cancellations, and terminal job failures are
not retried automatically.

Direct installs stream `install_transfer` JSON records while the payload is in
flight. Each record includes the phase, byte counts, percentage, elapsed time,
and average throughput. Cancelling or disconnecting the direct-install client
cancels only that install operation and leaves the warm outer session active.

An install is successful only when the command reaches `InstallComplete`, then
InstallationProxy reads back the same bundle identifier, version, and build
from the device. Deploy Link then returns an `installed` event. Opening the app
and checking its runtime behavior remains a separate test.

## Submit an unattended install

Use an install job when the terminal or agent should be free after submission.
`submit` validates the signed app, makes a private snapshot, records its full
content SHA-256 digest, and starts one macOS-owned one-shot LaunchAgent. The command returns a job ID, and
the one-shot process dispatches the job to the persistent daemon. The caller
can then disconnect without owning the transfer.

```sh
"$deploy_link" job submit --profile "$profile" /absolute/path/to/YourApp.app
"$deploy_link" job status --profile "$profile" <job-id>
"$deploy_link" job wait --profile "$profile" <job-id>
"$deploy_link" job remove --profile "$profile" <job-id>
```

`status` reads the durable receipt without taking over the job. `wait` follows
the same receipt until it reaches `installed`, `failed`, or `interrupted`.
Receipts can also show `queued` or `running`. Only one job can be active for a
profile; Deploy Link rejects a second submission instead of creating a queue.

The one-shot trigger uses `RunAtLoad` without `KeepAlive`. After the daemon
durably changes the receipt from `queued` to `running`, the daemon owns the
transfer and receipt even if the trigger disconnects. A bounded transient retry
is part of that one running job, but an interrupted or failed terminal job is
never restarted automatically. ZipConduit cannot resume, so each transfer
attempt begins from byte zero.
Before claiming the job, the daemon verifies the snapshot's bundle identity,
version, build, and content digest against the submitted receipt.
`installed` is written only after all of these checks pass:

- the random daemon instance ID and session generation have not changed;
- the profile install count has advanced by exactly one;
- InstallationProxy has read back the exact bundle identifier, version, and
  build.

The private app snapshot and receipt remain available until an explicit
`remove` cleans a terminal job. The Mac must stay awake and reachable, and the
profile's warm Deploy Link session must remain available while the job runs.
The job survives the submitting terminal or agent. A running transfer does not
continue through loss of the Apple session or a Mac shutdown.

## Install diagnostics

During an install, Deploy Link emits `event=install_transfer` records for the
payload, device-install, and readback phases. The safe fields include aggregate
byte counts, elapsed time, throughput, last-write age, generation, attempt
number, and an allowlisted Apple status. They intentionally omit app paths,
archive entry names, content, device identifiers, Tailnet addresses, and pairing
material.

Keep the complete daemon log owner-only and outside Git. Lower-level dependency
messages can still contain private local paths or network identifiers. If an
install stops, preserve the warm daemon and inspect the final safe phase before
changing a timeout, restarting, or trying a different delivery method.

## Install queue and iPhone app (no warm session needed)

When the warm session is cold (the device left Wi-Fi and Apple closed its
listener), builds can still reach a registered iPhone or iPad over Wi-Fi or
cellular through Apple's over-the-air install. Nothing here uses
RemotePairing; the device only needs Tailscale connected.

```text
signed .app -> ota-queue publish -> ~/.local/share/ios-ota/queue/<id>/
                                          |
        loopback server :8477 <- tailscale serve (tailnet-only HTTPS)
                                          |
          Deploy Link iPhone app -> Install -> iOS prompt -> download + install
```

- [`ota/ota_queue.py`](ota/ota_queue.py) packages a signed device `.app` (or
  takes an `.ipa`), records its identity and provisioning profile, keeps the
  newest five releases per app, and serves `/api/releases`, per-release
  manifests, icons and IPAs, plus an HTML list at `/` as a browser fallback.
  Each completed IPA download is appended to the release's `downloads.jsonl`,
  which is the Mac-side evidence that a device fetched the build.
- [`scripts/install_ota_queue.sh`](scripts/install_ota_queue.sh) installs it as
  `~/.local/share/deploy-link/bin/ota-queue` with the `com.10x.deploy-link-ota`
  LaunchAgent and makes sure `tailscale serve` proxies the Mac's tailnet HTTPS
  address to port 8477. Rerun it after changing the script.
- [`companion/`](companion/) is the Deploy Link iPhone app
  (`com.10x.deploylink`, XcodeGen `project.yml`). It lists the queue, refreshes
  every 15 seconds while open, and hands a release's `itms-services` link to
  iOS. It marks the build it is running, so publishing a newer build of the
  companion itself is the end-to-end test.

```sh
ota=~/.local/share/deploy-link/bin/ota-queue
"$ota" publish /path/to/App.app --note "What changed"
"$ota" list      # newest first, with download counts
"$ota" remove <release-id>
```

iOS always asks for one confirmation per install. Only devices in the app's
provisioning profile can install, and an install over the queue gives no
InstallationProxy readback; confirm the build on the device (the companion's
**Running** label, or the app's own version screen). Run
`python3 ota/test_ota_queue.py` for the fixture tests.

[`ota/ota_release.py`](ota/ota_release.py) is the earlier single-release
version of this lane. Generated OTA data belongs under `~/.local/share/ios-ota/`,
not in the repository. Do not use Tailscale Funnel for private development
builds.

## Earlier experiments

[`bridge.py`](bridge.py) relays and republishes a discovered RemotePairing
endpoint. It does not authenticate an RSD session or install an app by itself.

The [`ios/`](ios/) app tested an on-device packet reflector. On the tested iOS
27 build, cellular internet worked but Apple's RemotePairing and lockdownd ports
reset the connection. That experiment did not produce the successful cellular
install.

## Attribution

Deploy Link and its Link Core dependency retain their licenses and notices
inside [`deploy-link/`](deploy-link/). The on-device reflector is based on
[LocalDevVPN](https://github.com/jkcoxson/LocalDevVPN). See
[`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md).

## Status app and local verification

The status app provides **Import Profile**, a setup guide, and **Choose App…**
(⌘O) alongside drag and drop. Profile imports validate the schema and pairing
record locally before saving an owner-only copy. Invalid existing profiles
remain visible as actionable errors. Snapshot preparation shows progress and
can be cancelled; once a job has been submitted, refresh its receipt to check
its state. Cancelling a client command preserves the warm device bridge.

Status refreshes read the owner-only daemon socket directly. Completed receipts
are cached, with active/latest receipts refreshed and a full reconciliation on
manual Refresh, directory changes, or after one minute. Polling slows when the
app is inactive. Subprocess output is drained while commands run, with a bounded
256 KiB diagnostic tail and cancellation/deadline cleanup of their process group.

Run `deploy-link/scripts/test.sh` for Go tests, the race detector, and vet.
It prepares a checkout-specific Go workspace without installing or replacing
the live CLI. `DEPLOY_LINK_BUILD_ROOT`, `DEPLOY_LINK_DOWNLOADS_DIR`,
`DEPLOY_LINK_TOOLCHAINS_DIR`, `DEPLOY_LINK_SOURCES_DIR`, and standard Go cache
variables support separate build volumes and shared pinned dependencies.
Run `deploy-link/scripts/test-status-app.sh` for process, local socket, and
receipt-cache fixtures; `TEST_BUILD_DIR` selects its output folder. These local
checks do not establish physical-device or visual/accessibility acceptance.
