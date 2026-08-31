# Deploy Link Architecture

Document revision: `0.8.0`

Revised: `2026-08-31`

## Decision

Deploy Link runs one unprivileged, launchd-managed supervisor for each device
profile on macOS. Each supervisor owns one verified RemotePairing control
connection, one TLS-PSK data connection, one userspace RSD topology, and
serialized app-install requests. Tailscale supplies stable unicast reachability
but is not pairing, session, or installation authority.

The CLI is a short-lived client. It communicates only through a profile-scoped,
owner-only Unix socket. No control API is exposed over TCP.

Install diagnostics use bounded structured events. They record only phase,
byte counts, elapsed time, throughput, generation, attempt number, and an
allowlisted Apple status. They do not record app paths, archive entries,
pairing material, device identifiers, or Tailnet addresses.

## Build and install ownership

| Stage | Authority |
| --- | --- |
| Source compilation | Xcode / `xcodebuild` |
| Development signing | Xcode and Apple's signing assets |
| Bridge reachability | Tailscale plus the configured iPhone Tailnet IP |
| Pair verification | The configured owner-only RemotePairing record |
| Warm session | The long-running Deploy Link daemon |
| App transfer | Streaming zip conduit over the live RSD session |
| Success readback | InstallationProxy bundle enumeration |

Deploy Link consumes an existing signed `.app`. It does not become an Xcode Run
Destination, select a signing team, modify a project, or weaken iOS signature
validation.

## Session lifecycle

```text
waiting_for_listener -> connecting -> active -> installing -> active
                              |          |            |
                              |          +------------+ service failure
                              |                       v
                              +-----------------> recovering
                                                       |
                                    later install -----+----> active

outer packet pump terminates -> session_lost -> waiting_for_listener
```

Each profile has a stable launchd identity derived from its absolute path, a
separate control socket, a separate local forward port, and separate logs. A
supervisor waits when an existing foreground daemon owns the socket, then takes
over after it ends. This permits service installation without interrupting a
working warm session. While on AC power, the supervisor requests that macOS not
enter idle system sleep. Closing a MacBook lid or restarting macOS can still end
the Apple session.

Advisory locks give each profile socket and each local forward port one owner.
They prevent two device services from silently unlinking the same socket or
claiming the same local port. Profile locks remain beside the owner-only
profiles. Port locks contain no device data and use the current user's temporary
directory, so normal operating-system temporary-file cleanup may remove them
after no process owns them.

LaunchAgent installation is idempotent. It does not restart an already loaded
profile service. A deliberate service upgrade must be scheduled for a safe
acquisition window because replacing its process ends the current warm session.

An InstallationProxy, ZipConduit, or RSD request failure does not by itself
destroy the outer warm tunnel. Only termination of the outer packet-dispatch
loop releases the session. This prevents a recoverable service error from being
misreported as successful connectivity while also avoiding self-inflicted cold
reacquisition.

## Network boundary

RemotePairing acquisition requires iOS to expose its listener. Any Wi-Fi
connection can activate it, and Tailscale lets the Mac reach the explicit
iPhone endpoint without Bonjour or a shared LAN. Once verified, both outer TCP
connections remain open while Tailscale changes underlay from Wi-Fi to cellular.

This is warm-session continuity, not pure-cellular discovery. If the outer
session terminates while the phone is cold on 5G, the daemon waits until Wi-Fi
causes iOS to expose RemotePairing again.

If Apple's listener moves to another dynamic port, the daemon uses a bounded,
protocol-verified scan of the known RemotePairing port range. A learned port is
kept only in daemon memory and never writes pairing or profile data. Discovery
backs off from 15 minutes to one hour after failures, and a trusted session
resets that backoff. It can find a listener that iOS already exposes; it cannot
make iOS expose one on cellular.

## Source and dependency boundary

- Public source mirror:
  `zjz-connect/xcode-deploy-link-deploy-beyond-local-network`;
- Git fetch authority: `/srv/contabo/git/deploy-link.git`;
- Go toolchain: `1.26.5` for macOS arm64;
- Link Core upstream: `danielpaulus/go-ios` commit
  `3ebc297691a9e364772aef027744ebc0c49421a5`;
- maintained downstream change: `patches/link-core-tailnet.patch`.

The installer verifies the Go archive checksum, verifies the exact upstream
commit, and fails if the patch no longer applies cleanly. Generated dependency
source, caches, profiles, pairing records, logs, and binaries remain outside
Git under `~/.local/share/deploy-link/`.

## Security invariants

- Pairing records and profiles must be owner-only regular files.
- Every simultaneously managed device must use a different local forward port.
- LaunchAgent output uses an owner-only umask, and continuous tunnel telemetry
  is disabled for supervised services.
- Pairing private key material is never copied into a profile, log, or response.
- The RSD-reported identifier must match the configured RemotePairing identifier.
- Install requests are serialized and accept only an existing, code-signed
  `.app` directory.
- Success requires post-install bundle readback.
- No command changes Tailscale ACLs, enables Developer Mode, edits Apple's USB
  pairing database, republishes Bonjour, or falls back to TestFlight.
