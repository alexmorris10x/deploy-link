# App Store listing draft

**Status (2026-10-30):** Not submitted. The Developer Program agreement is accepted, and Paid Apps and Free Apps agreements are Active. The [installation-policy and reviewer-provisioning question](app-review-question.md) was sent to Apple Developer Support as case **103013688812**; a reply is pending.

| Field | Value |
| --- | --- |
| Name | Deploy Link |
| Subtitle | Your private iOS build queue |
| Category | Developer Tools |
| Price | Free; no in-app purchases or subscription |
| Bundle ID | `com.10x.deploylink` |
| Minimum OS | iOS / iPadOS 17 |
| Marketing URL | `https://alexmorris10x.github.io/deploy-link/` |
| Support URL | `https://alexmorris10x.github.io/deploy-link/support.html` |
| Privacy URL | `https://alexmorris10x.github.io/deploy-link/privacy.html` |
| Copyright | 2026 Alex Morris |
| Keywords | `developer,builds,queue,deploy,ios,ipad,testing,mac,tailscale,private` |

## Promotional text

Your signed iOS builds in one private queue. See versions, notes, and download progress from your own Mac over Tailscale. Free and open source.

## Description

Build on your Mac. Choose the build on your iPhone or iPad.

Deploy Link connects to your own private queue of signed iOS development builds. Browse apps, check versions and build notes, and choose a build to pass to the iOS installation prompt.

• Latest builds first, with older releases grouped by app.
• Version numbers, build numbers, release notes, and icons.
• Transfer progress and completed-download history for this device.
• Optional management access for removing queued builds.
• A free, open-source Python server you run on your Mac.

YOUR SERVER, YOUR BUILDS

Enter your own HTTPS queue address. There is no Deploy Link account or shared hosting service. The recommended setup uses Tailscale on your Mac and mobile device, with Tailscale Serve providing private HTTPS access. Tailscale has separate plans and terms.

SIGNING STILL MATTERS

Deploy Link does not sign your apps or bypass Apple's installation rules. Each build must have valid signing and provisioning for its intended devices and distribution method. Your Mac must remain awake and online during downloads.

Downloaded means the file transfer finished. iOS controls whether installation succeeds; Deploy Link cannot read back that result or inspect the apps installed on your device. Open your target app to confirm it installed.

Free to use. No subscription or in-app purchase. Both the iOS client and Mac server are open source under Apache 2.0.

Setup, privacy, and support:
https://alexmorris10x.github.io/deploy-link/

## Review notes draft

Deploy Link is a client for a developer's own private signed-build queue, hosted by that developer on a Mac. It is not a public app catalog or an app-signing service. The client lists build metadata through HTTPS. On the user's explicit Install action, it opens an `itms-services` URL pointing to that build's HTTPS manifest. iOS presents and handles the installation; the client does not execute downloaded code inside its process or bypass certificate, provisioning, or device eligibility checks.

The client has no preconfigured personal server. The user provides their own HTTPS server URL. The recommended network is Tailscale; a separate management token is required only to delete queue entries. A completed server transfer is labeled Downloaded because iOS does not report installation completion back to the client.

Reviewer-owned setup requires a Mac with full Xcode, Python 3.9 or newer, Tailscale on the Mac and review device, and a harmless sample signed for that device and distribution method. These instructions and the pending support case are saved in App Store Connect; clean customer onboarding and reviewer installation access have not been exercised.

1. Obtain the [source](https://github.com/alexmorris10x/deploy-link), inspect the installer, and run `bash scripts/install_ota_queue.sh` from the repository in a clean Mac login. It installs the user LaunchAgent and creates a private Tailscale Serve HTTPS endpoint.
2. Publish the signed sample with `"$HOME/.local/share/deploy-link/bin/ota-queue" publish "/path/to/ReviewSample.app" --note "Harmless review sample"`.
3. Enter the installer's HTTPS root address in the iOS app's **Settings**. Browse the sample and its version notes, choose **Install**, and respond to the system prompt. Open the sample afterward to confirm installation; **Downloaded** records transfer completion only.
4. To test optional queue management, run `"$HOME/.local/share/deploy-link/bin/ota-queue" admin-token --copy` on the Mac, transfer it privately into **Management token**, and clear the clipboard. The token is needed only for deletion; remove only the disposable review sample.

[Full setup instructions](https://alexmorris10x.github.io/deploy-link/setup.html). This route is usable only with a sample signed for the review device; a build signed solely for the developer's devices is insufficient. If App Review cannot supply/sign its own sample, resolve the supported provisioning arrangement with Apple before claiming full review access. Case **103013688812** asks about that arrangement and the native `itms-services` policy; a pending question is not approval. Keep private builds, credentials, provisioning profiles, tokens, and real device identifiers out of this repository. A recording can provide context but cannot replace functional access.

## Release inputs still needed

- Apple's response on the installation flow, including guideline 2.5.2 and sample provisioning; [case 103013688812](app-review-question.md) is pending.
- Genuine iPhone/iPad screenshots and exercised review access with an eligible signed sample. Build `20261030055825` is selected; privacy, age-rating, export-compliance, free pricing, and manual release are configured in App Store Connect.
- Replace the published site's pending App Store text only after the listing is actually published; website, support, and privacy pages are already live.

The source has no tracking or developer telemetry endpoint. Its user-run server stores build metadata and transfer/device records, and the public privacy policy describes this. Complete App Privacy against the exact submitted build and current questionnaire rather than treating that statement as a prefilled answer.

Alex handles TestFlight and App Store submission. The support question was sent by email through Apple Developer Support; no appointment was booked and no submission or approval is claimed.
