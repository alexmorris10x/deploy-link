# Launch distribution

Deploy Link is initially free: the iOS client has no in-app purchase, and the Mac Python server is installed from source. Both are Apache 2.0. App Store policy for the install action remains an open launch input; [the exact question](app-review-question.md) is prepared for Alex, not sent.

## Public site and source

The site lives in `docs/site/`, with local assets and no build dependencies. `.github/workflows/pages.yml` publishes that folder to `https://alexmorris10x.github.io/deploy-link/` using official Pages actions and runs only when the repository is public. Keep Actions disabled while private. After public-source preparation, enable Actions, select GitHub Actions as the Pages source, and dispatch the workflow once. No paid hosting or domain is required.

The page intentionally has no App Store badge or invented app ID. Add the real listing link only after publication. The server instructions link to the repository and locally run `bash scripts/install_ota_queue.sh`; they do not download and execute an unseen script through a shell pipe.

## Runtime contract

- Python 3.9 or newer on macOS; Tailscale installed and connected. Installer: `bash scripts/install_ota_queue.sh`.
- Default runtime: `~/.local/share/deploy-link/`; default queue: `~/.local/share/ios-ota/queue/`; LaunchAgent: `com.10x.deploy-link-ota`.
- Default loopback port: 8477, reached through the user's private Tailscale HTTPS address. Do not use Funnel or expose the queue publicly.
- Queue listing and downloads use Tailscale/network access rules. `DELETE /api/releases/{id}` and `GET /api/management` require `Authorization: Bearer <management-token>`. Tokens do not belong in query strings or logs.
- The server stores its token at `<queue-root>/.management-token` with owner-only permissions. `ota-queue admin-token --copy` copies it locally without printing it. For a custom queue: `ota-queue --root <queue-root> admin-token --copy`.
- Installer overrides, when needed: `DEPLOY_LINK_RUNTIME_ROOT`, `DEPLOY_LINK_QUEUE_ROOT`, `DEPLOY_LINK_OTA_PORT`, `DEPLOY_LINK_OTA_BASE_URL`, `DEPLOY_LINK_PYTHON`, and `DEPLOY_LINK_TAILSCALE`. A custom base URL must be an HTTPS host root with no credentials, query, or fragment.
- Publishing retains the newest five builds per app by default; pruning and expired-profile removal can delete queued packages. The public installer does not add a nightly pruning schedule. Do not copy a personal machine's cleanup schedule into the public setup requirements.

The completed-download record is not installation proof. Validate a real installation by opening the target app, and keep that fact separate from successful queue listing or file transfer.

## License inventory

| Component | Treatment |
| --- | --- |
| Original Swift client, Python server, scripts, docs, icon | Apache 2.0; preserve `LICENSE` and `NOTICE`. |
| Apple frameworks | Supplied by Apple SDKs/OS; no framework source or binaries vendored here. |
| Python and its standard library | Installed separately; no pip packages or bundled Python runtime. |
| Tailscale | Installed separately under its own terms; no binary or SDK included. |
| XcodeGen / GitHub Pages actions | External build/deploy tools, not app runtime dependencies. |

The checked source has no Swift Package, CocoaPods, Carthage, or pip runtime dependency and no conflicting third-party notice was found. The owner must still have publication rights to the original source/artwork and prepare repository history for public visibility. This is a license inventory, not a security audit.

Use [the metadata draft](app-store-metadata.md) for the listing and review-access inputs. Signing, App Review access, and submission remain separate from publishing the open-source server and website.
