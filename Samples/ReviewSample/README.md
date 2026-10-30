# Deploy Link review sample

A small, independent iPhone/iPad app for testing the complete build-queue installation flow. Opening it shows **Sample opened**, its version/build, and a button whose counter changes. It makes no network requests, asks for no permissions, stores no data, and contains no account or purchase flow.

The source is covered by this repository's Apache 2.0 license. No signing identity, provisioning profile, private key, or pre-signed app is included. A sample signed for one developer's devices cannot be assumed installable on somebody else's device.

## Build and sign for your device

1. Install full Xcode and XcodeGen, then run `xcodegen generate` in this folder and open `DeployLinkReviewSample.xcodeproj`.
2. In **Signing & Capabilities**, choose your Apple Developer team and set a unique bundle identifier you control. Keep automatic signing enabled.
3. Connect the intended iPhone or iPad, enable Developer Mode when required, select that device in Xcode, and build the app. Use a signing/provisioning method permitted for your device and distribution. Ad hoc distribution requires the device to be registered in the provisioning profile; this sample does not change Apple's requirements.
4. Locate the signed `DeployLinkReviewSample.app` in Xcode's build products. In the Deploy Link Mac companion, choose **Add Build…** and select it. The source-install equivalent is:

   ```sh
   "$HOME/.local/share/deploy-link/bin/ota-queue" publish "/path/to/DeployLinkReviewSample.app" --note "Harmless installation sample"
   ```

5. In Deploy Link on the eligible device, connect to your queue, select the sample, choose **Install**, and accept the iOS prompt. After installation, open **Deploy Link Sample** from the Home Screen and tap the test button. Seeing **Sample opened** and the counter change verifies that the installed app launches. A **Downloaded** label alone only establishes transfer completion.

For an update check, increase the build number, rebuild with the same identity and bundle identifier, publish again, and confirm the new build number after installing and opening it. Delete only your disposable sample when testing queue management.

## App Review access

This source gives a reviewer a harmless app they can sign for an eligible device. It does not prove App Review will accept reviewer-owned signing, supply a device identifier, or approve Deploy Link's installation flow. Those arrangements remain subject to Apple's response in support case **103013688812**. See [the review question](../../docs/release/app-review-question.md) and [setup instructions](../../docs/site/setup.html).
