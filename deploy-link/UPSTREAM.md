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
  `78e96fa84ba4f15116ca5fc978c75383f2f9cacd523de02284aa3d12691663d9`
- license: MIT, Copyright (c) 2019 danielpaulus

The downstream source adds structured, privacy-filtered install-transfer
diagnostics in `internal/linkcore/daemon.go`, `session.go`, and
`install_diagnostics.go`. The Link Core patch adds the explicit Tailnet dial
path and byte-progress callback needed by those diagnostics. The upstream
commit above remains the import base, not a claim that this tree is unchanged.

No live runtime files were copied into this directory. Pairing records,
profiles, caches, logs, downloaded toolchains, and compiled binaries stay under
the user's private local application-state directory.
