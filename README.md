# Remote iOS Iteration Prototype

This repository tests direct iOS development installs when the Mac and iPhone
are on different physical networks.

## Verified result

The fastest proven lane is a warm Apple RemotePairing session carried by
Tailscale. Xcode still builds and signs the app. The vendored Deploy Link daemon
keeps the authenticated Apple session open while the iPhone moves from Wi-Fi to
cellular, then installs the signed `.app` through Apple's normal installation
services.

```text
Xcode signed .app
       |
       v
Deploy Link CLI -> owner-only local socket -> warm Apple tunnel
                                                |
                                                v
                                      Tailscale -> iPhone
```

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

- Start Deploy Link while the iPhone is on any Wi-Fi and Tailscale is connected.
- Wait until the bridge reports `active`.
- The iPhone can then move to cellular and accept repeated direct installs.
- If the Apple session ends, the iPhone must join any Wi-Fi so the daemon can
  acquire a new RemotePairing listener.

The iPhone does not need to share the Mac's local network. This prototype does
not yet provide a native Xcode Run Destination, LLDB debugging, or live device
console access.

## Source ownership

The exact successful Deploy Link source is maintained under [`deploy-link/`](deploy-link/).
It is a downstream copy based on:

- source: `zjz-connect/xcode-deploy-link-deploy-beyond-local-network`;
- commit: `66a39ee4da4ac7f6ad26c871552120ad56ead92b`;
- version: `0.7.0`;
- Link Core: `danielpaulus/go-ios` at
  `3ebc297691a9e364772aef027744ebc0c49421a5`; and
- Link Core patch SHA-256:
  `78e96fa84ba4f15116ca5fc978c75383f2f9cacd523de02284aa3d12691663d9`.

The maintained downstream changes add the Tailnet connection path and bounded,
privacy-filtered install-transfer diagnostics used to distinguish payload,
device-install, and readback progress. They do not add a remote control API.

See [`deploy-link/UPSTREAM.md`](deploy-link/UPSTREAM.md) and the included license
files. Profiles, pairing records, logs, binaries, toolchains, and build caches
must stay outside Git.

## Install the host tool

```sh
./scripts/install_deploy_link.sh
```

The wrapper uses the vendored, pinned source and keeps generated state under:

```text
~/.local/share/deploy-link/
```

That directory is durable local tool state. Keep it while the bridge is in use.
Remove it only when intentionally uninstalling Deploy Link. It can contain a
private Apple pairing record and must never be committed or shared.

## Pair and configure one iPhone

Create a RemotePairing record while the iPhone is available locally. Follow the
[`pymobiledevice3` iOS 17+ tunnel guide](https://github.com/doronz88/pymobiledevice3/blob/master/docs/guides/ios17-tunnels.md).
On current iOS, the matching phone screen is:

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

## Start a warm session

Keep the iPhone on Wi-Fi and Tailscale for this step. Run the daemon in the
foreground during prototype testing:

```sh
"$deploy_link" serve --profile "$profile"
```

In another terminal:

```sh
"$deploy_link" status --profile "$profile"
```

Do not switch the iPhone to cellular until the state is `active`. The launch
agent path is included in the vendored module, but it is not part of the
verified proof yet.

## Build and install over cellular

Build one signed iPhone app with the project's normal Xcode development lane.
After Deploy Link is active, turn off iPhone Wi-Fi, leave Tailscale connected,
and install the signed app output:

```sh
"$deploy_link" install \
  --profile "$profile" \
  /absolute/path/to/YourApp.app
```

An install is successful only when the command reaches `InstallComplete` and
returns an `installed` event. Exact build confirmation is separate: read the
visible Version and Build rows in the app, or use device readback after Apple
CoreDevice becomes available again.

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

## Private OTA fallback

[`ota/ota_release.py`](ota/ota_release.py) provides an independent registered-device
OTA lane through private Tailscale HTTPS. It is useful when no warm session is
available, but it requires an install tap and does not provide debugging or
console access.

Generated OTA data belongs under `~/.local/share/ios-ota/`, not in the
repository. Do not use Tailscale Funnel for private development builds.

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
