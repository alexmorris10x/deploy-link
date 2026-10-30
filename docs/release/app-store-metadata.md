# App Store listing draft

**Status:** Prepared for launch; not submitted. App Review guidance on the installation workflow remains unresolved. See [the appointment question](app-review-question.md).

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

Review access: [FILL PRIVATELY with an isolated test queue containing a harmless sample app, a working HTTPS/network access route, signing/provisioning arrangements appropriate for the reviewer's device, and a contact who can resolve access problems]. Do not put private build files, credentials, provisioning profiles, tokens, or real device identifiers into the repository. If the chosen sample cannot be provisioned for the reviewer's device, resolve that with App Review before representing the flow as reviewable. Attach a short recording as context, not as a replacement for functional access.

## Release inputs still needed

- App Review guidance on the exact installation flow, including guideline 2.5.2; the [question draft](app-review-question.md) is ready for Alex to use.
- A selected release build, genuine iPhone/iPad screenshots, usable isolated review access, and current App Store privacy/age-rating/export-compliance answers.
- Live website/support/privacy URLs. Replace the site's pending App Store text only after the listing is actually published.

The source has no tracking or developer telemetry endpoint. Its user-run server stores build metadata and transfer/device records, and the public privacy policy describes this. Complete App Privacy against the exact submitted build and current questionnaire rather than treating that statement as a prefilled answer.

Alex handles TestFlight and App Store submission. No appointment has been booked or message sent by preparing these files.
