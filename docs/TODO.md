# Deploy Link launch work

Updated 2026-10-30. Remaining work only; the 25 Python tests and signed iOS device build already passed for this change.

- [ ] Resolve App Review's treatment of this native client's development/ad hoc `itms-services` workflow, including any permissible Safari alternative and reviewer-device provisioning. Use the [specific question draft](release/app-review-question.md); no appointment has been booked or question sent.
- [ ] Verify the published Apache 2.0 installer instructions against a separate clean Mac setup. The local server update succeeded while preserving the queue and Tailscale endpoint.
- [ ] Run a customer-style setup using the user's own HTTPS address: browse/download with no management token, authorize and confirm deletion with a matching token, and verify an appropriately signed sample actually opens after installation. A Downloaded label is insufficient evidence.
- [ ] Capture genuine iPhone/iPad screenshots and prepare an isolated review queue with a harmless sample, functional network access, and a provisioning arrangement App Review can use. Keep private tokens/device records outside the repository.
- [ ] Alex must accept the updated Apple Developer Program agreement. Listing copy, free pricing, 175-territory availability, privacy disclosures, export-compliance answer, age rating and manual release are configured for `6828728411`.
- [ ] Replace draft review-access notes with the tested arrangement. Release build `20261030055825` is valid and attached in App Store Connect, with draft notes and the existing account contact. Alex handles TestFlight/App Store submission and release; neither has occurred yet. Add an App Store website link only after publication.
