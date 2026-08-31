# Vendored source record

This directory is a maintained downstream copy of Deploy Link.

- Upstream repository:
  `https://github.com/zjz-connect/xcode-deploy-link-deploy-beyond-local-network.git`
- Upstream commit:
  `66a39ee4da4ac7f6ad26c871552120ad56ead92b`
- Imported: `2026-08-30`
- License: MIT, Copyright (c) 2026 zjz-connect

The installer pins Link Core to:

- repository: `https://github.com/danielpaulus/go-ios.git`
- commit: `3ebc297691a9e364772aef027744ebc0c49421a5`
- patch SHA-256:
  `4dc515286bf1fce9df7191daf9289c55e77e7c6d73b35c9388b5bc5237a6e5ed`
- license: MIT, Copyright (c) 2019 danielpaulus

The downstream source adds structured, privacy-filtered install-transfer
diagnostics in `internal/linkcore/daemon.go`, `session.go`, and
`install_diagnostics.go`. The Link Core patch adds the explicit Tailnet dial
path, context-bounded service setup, and byte-progress callback needed by those
diagnostics. The upstream commit above remains the import base, not a claim that
this tree is unchanged.

No live runtime files were copied into this directory. Pairing records,
profiles, caches, logs, downloaded toolchains, and compiled binaries stay under
the user's private local application-state directory.
