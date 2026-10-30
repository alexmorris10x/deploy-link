# Question for App Review

**Status:** Draft only. No appointment booked or message sent. An appointment gives guidance, not preapproval.

Use [Apple's App Review appointment route](https://developer.apple.com/help/app-review/before-submitting-for-review/app-review-appointment).

> We are preparing a free developer utility called Deploy Link. A developer runs its open-source Python server on their own Mac, with access restricted to their Tailscale network. The iOS client has no shared catalog or default server; the developer enters their own HTTPS URL and sees their own signed development or ad hoc builds, version notes, and transfer progress.
>
> When the user taps Install, the client opens an `itms-services://?action=download-manifest&url=…` link. iOS, rather than the client, retrieves the HTTPS manifest/package, presents the system prompt, and applies its normal signing, provisioning, and device-eligibility checks. The client neither signs apps nor executes downloaded code. It labels transfer completion Downloaded and does not claim to know installation succeeded.
>
> Is this native-client workflow permitted on the App Store under guideline 2.5.2? If it is, what scope, restrictions, disclosures, or review-access setup do you require for a private development/ad hoc build queue? In particular, how should we provide an installable sample to reviewers when device provisioning may require their device to be registered?
>
> The server also has a Safari queue page. If the native app may show build metadata but cannot open the installation URL, would a user-initiated link to that private Safari page be permissible, or would the native client need to omit installation access entirely?

Bring a short recording of publishing a harmless signed sample, configuring the HTTPS URL, choosing its install action, and the system prompt. Show the actual network and provisioning requirements; do not describe private build distribution as unrestricted consumer sideloading. Follow the answer rather than silently disguising the installation action.

Apple's [App Review Guidelines](https://developer.apple.com/app-store/review/guidelines/) establish the relevant executable-code restriction. Apple's [in-house app distribution documentation](https://support.apple.com/guide/deployment/distribute-proprietary-in-house-apps-depce7cefc4d/web) describes HTTPS manifests and system install links for a specific distribution program; that document does **not** establish App Store approval for this client or general permission to distribute development/ad hoc apps. Record the precise response here after the appointment, including any conditions and outstanding questions.
