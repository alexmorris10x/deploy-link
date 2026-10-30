# Deploy Link launch work

Updated 2026-10-30. Remaining work only; the 25 Python tests and signed iOS device build already passed for this change.

- [ ] Resolve App Review's treatment of this native client's development/ad hoc `itms-services` workflow, including any permissible Safari alternative and reviewer-device provisioning. Use the [specific question draft](release/app-review-question.md); no appointment has been booked or question sent.
- [ ] Publish the intended Apache 2.0 source and static setup/privacy/support pages. Verify the public installer instructions against a separate clean Mac setup; preserve unrelated Tailscale routes and existing queue data on update.
- [ ] Run a customer-style setup using the user's own HTTPS address: browse/download with no management token, authorize and confirm deletion with a matching token, and verify an appropriately signed sample actually opens after installation. A Downloaded label is insufficient evidence.
- [ ] Capture genuine iPhone/iPad screenshots and prepare an isolated review queue with a harmless sample, functional network access, and a provisioning arrangement App Review can use. Keep private tokens/device records outside the repository.
- [ ] Finish record `6828728411` metadata, privacy/age-rating/export-compliance answers, and any outstanding Apple agreements; Alex handles account-holder agreement acceptance.
- [ ] Attach the release build and complete review notes, then leave TestFlight/App Store submission and release to Alex. No release upload or submission has occurred yet. Add an App Store website link only after the app is actually published.
