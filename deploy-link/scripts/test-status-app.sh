#!/bin/bash
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd -P)"
output="${TEST_BUILD_DIR:-${root}/.build/status-tests}"
mkdir -p "$output"
xcrun swiftc -O "$root/status-app/BoundedProcess.swift" "$root/status-app/ReceiptCache.swift" "$root/status-app/LocalStatusSocket.swift" "$root/status-app/Tests/main.swift" -o "$output/fixtures"
"$output/fixtures"
