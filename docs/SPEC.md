# Deploy Link blueprint

Version 1, 2026-10-30. Owner: Alex. Update this file when behavior, access, pricing, or distribution changes.

## 1. The product in one minute

Deploy Link is a private signed-build queue for an iOS developer's own devices. A free native Mac app and menu-bar item manage the Python queue server; the free iPhone/iPad client lists them and hands a chosen install link to iOS. It removes the need to find the right build URL repeatedly. The initial release has no subscription or in-app purchase. Both components use Apache 2.0 source; App Store acceptance of the installation workflow remains unresolved.

## 2. Premise and hook

- **Premise:** a developer has a signed build on their Mac and wants the right version on a phone or iPad without a repeated file/link hunt. Existing personal use establishes the workflow, not external demand.
- **Hook:** publish a harmless signed build once, find it by version and note on the phone, and show iOS completing the installation. Keep transfer evidence separate from the final installed-app check.

## 2a. The bet

The first audience is iOS developers comfortable managing signing and Tailscale. The useful result is a reachable private build queue with clear version and transfer status. Risks are signing/setup friction and whether Apple permits this native client; the Safari queue is an existing interface, not proof of native policy acceptance.

## 3. The offer and price

Free initially for iOS, the Mac app, and the queue server. No trial, payment SDK, subscription, account service, or hosted storage tier. The user supplies their own Mac, network access, valid build signing/provisioning, and any required Apple/Tailscale arrangements. A future commercial offer would require a new product decision.

## 4. Unit costs and margin

Storage, packaging, and bandwidth run on the user's Mac/network. There is no per-download developer-hosted service. Apple program costs and the user's Tailscale plan apply separately; no margin or acquisition cost is asserted for a free launch. The static public site uses GitHub Pages without a new paid platform/domain.

## 5. Getting customers

Publish the source and setup page, then the native listing if approved. Demonstrate one real publish-to-install flow with synthetic data. Broad growth campaigns, analytics registration, and outreach are not part of this launch implementation; no demand or conversion figures are assumed.

## 6. Feedback path

Have a developer publish one of their own appropriately signed apps and identify the first step they cannot complete without help. Ask whether the build/version history prevents mistakes and whether download status is clear. Public support uses GitHub Issues with private details removed. Alex owns outbound messages.

## 7. Platforms and infrastructure

Mac server: Python 3.9+, standard library only, loopback HTTP 8477 behind private Tailscale Serve HTTPS on port 8443 by default, leaving other services on port 443 intact. Explicit HTTPS ports are supported. iOS/iPadOS 17+ SwiftUI client. Defaults: runtime `~/.local/share/deploy-link/`, queue `~/.local/share/ios-ota/queue/`, LaunchAgent `com.10x.deploy-link-ota`. The macOS 14+ SwiftUI companion provides setup, status, start/stop, separate app/server login controls, queue-address copy, build publishing, and token copy. It preserves the existing queue and uses the installed Python 3.9+ runtime and full Xcode. Distribution is a universal Developer ID-signed, notarized direct download, with the source installer retained as an alternative; it is not a Mac App Store app. Native App Store Connect record: `6828728411`; no submitted release is implied. Website: `https://alexmorris10x.github.io/deploy-link/`.

## 8. Brand and look

Deploy Link uses the jade download-ribbon/phone icon and native grouped build lists. Labels are **Install**, **Update**, **Downloaded**, and **Running** for this app's known current build. Never label another app Installed based only on an IPA transfer.

## 9. Rules for anyone changing it

- No personal/default host: the user supplies a valid HTTPS server root, without embedded credentials, query, or fragment.
- Read/download access follows network rules. Optional management credentials authorize deletion; they do not protect a publicly exposed queue's read access.
- Store the iOS token in Keychain for its server. Use an Authorization header, never a query parameter, and never log or display it.
- `Downloaded` means completed transfer, not verified installation. Signing and provisioning remain Apple's checks.
- Preserve queue data/token on updates, do not reset unrelated Tailscale Serve mappings, and do not enable Funnel for this private queue.
- Preserve Dev/Release identities and keep private binaries, provisioning files, device records, and secrets out of public source/support.

## 10. Features

| Feature | Current state | Main source |
| --- | --- | --- |
| Package signed `.app`/`.ipa`, inspect metadata, publish queue | Implemented | `ota/ota_queue.py` |
| Native Mac window and menu-bar controls | Implemented | `mac/DeployLinkMac`, `mac/Resources/mac_control.py` |
| Harmless self-signable review sample | Source provided; device install still to verify | `Samples/ReviewSample/` |
| Private HTTPS setup and login service | Implemented | `scripts/install_ota_queue.sh` |
| Build grouping, progress, completed-download history | Implemented | `ReleaseStore.swift`, `ReleaseListView.swift`, server |
| User-provided server and optional token configuration | Implemented | iOS settings/store |
| Authenticated queue deletion and local token rotation | Implemented | server management routes and command |
| Safari queue page | Implemented | server HTML response |
| Native App Store distribution | Prepared; policy and review acceptance pending | [Review question](release/app-review-question.md) |

Publishing keeps only the newest build per app by default; `--keep N` explicitly retains more. Expired releases leave the queue. Device history survives release deletion. The public installer adds no automatic nightly pruning schedule.

## 11. Screens

The Mac window shows prerequisites, queue status/address, latest builds, Add Build, separate app/server login switches, and optional management-access copy. Its menu-bar item exposes the queue, Add Build, address copy, and start/stop. Closing or quitting the app leaves an independently configured server running. An empty iOS client explains setup and offers **Connect Your Mac**. Settings contains **Mac address**, **Management token (optional)**, save/cancel, and privacy/support links. The queue groups releases by app with notes and transfer status. Reinstall/downgrade actions require confirmation; queue removal states that it deletes the server copy for all devices, without uninstalling an app.

## 12. Quality bar

A new user can configure their own queue, browse and download without a token, authorize deletion with the matching token, and understand that installation is unconfirmed until checked on the target device. Invalid URLs and cross-server credentials must not leak secrets. The launch change's 25 Python tests and signed iOS device build passed. Real onboarding, installation with appropriate provisioning, screenshots, review access, and policy acceptance remain in [TODO](TODO.md).

## 13. Standard parts

| Part | Status |
| --- | --- |
| Dev/production; XcodeGen | Separate Debug/Release identities; implemented |
| Payments; internal payers; transactional email | Not applicable to this free, accountless launch |
| Public page | Live on GitHub Pages; native App Store listing link pending |
| Analytics; feedback pass; waitlist | No tracking SDK, paid gate, or waitlist; free use supports evaluation |
| Referrals; affiliates | Not now |
| Blueprint and backlog | This file and TODO are canonical |

## 14. Not now

Public build marketplace, signing service, bypassing device/provisioning restrictions, hosted storage, payments, Windows/Linux installer, and new automatic cleanup jobs. A native-to-Safari alternative must follow Apple's guidance, not disguise the install action.

## 15. Decisions and remaining questions

**Decided (2026-10-30):** free launch, user-owned HTTPS server, optional management token, accurate transfer wording, Apache 2.0 source, a native Mac app/menu-bar item with a signed direct download, and an optional source installer. **Assumptions:** developers will accept the prerequisite setup and find the queue useful. **Open external question:** App Review treatment of native `itms-services` links and review-device provisioning; the [question](release/app-review-question.md) was sent as Apple Developer Support case `103013688812` on 2026-10-30; reply pending. The Developer Program agreement is accepted, and the Free/Paid Apps agreements are Active. Submission and release remain separate actions.
