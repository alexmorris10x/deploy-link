#!/bin/bash

set -euo pipefail

script_directory="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repository_root="$(cd "${script_directory}/.." && pwd -P)"
source_root="${repository_root}/deploy-link/status-app"
: "${HOME:?HOME must be set}"
runtime_root="${DEPLOY_LINK_RUNTIME_ROOT:-${HOME}/.local/share/deploy-link}"
app_path="/Applications/Deploy Link.app"
executable_path="${app_path}/Contents/MacOS/DeployLink"
legacy_app_path="${runtime_root}/status/Deploy Link Status.app"
legacy_launch_agent_path="${HOME}/Library/LaunchAgents/one.zjz.deploy-link.status.plist"
legacy_launch_target="gui/$(id -u)/one.zjz.deploy-link.status"
stage_root="$(mktemp -d "${runtime_root}/build/.status-app.XXXXXX")"
install_stage="/Applications/.Deploy Link.install.$$.app"
install_backup="/Applications/.Deploy Link.backup.$$.app"
install_committed=0

cleanup() {
  /bin/rm -rf -- "${stage_root}" "${install_stage}"
  if [[ -d "${install_backup}" && "${install_committed}" -ne 1 ]]; then
    /bin/rm -rf -- "${app_path}"
    /bin/mv "${install_backup}" "${app_path}"
  fi
}
trap cleanup EXIT

stage_app="${stage_root}/Deploy Link.app"
mkdir -p "${stage_app}/Contents/MacOS"

/usr/bin/xcrun swiftc \
  -O \
  -parse-as-library \
  -target arm64-apple-macos13.0 \
  -framework AppKit \
  -framework CryptoKit \
  -framework SwiftUI \
  -framework UniformTypeIdentifiers \
  "${source_root}/DeployLinkStatusApp.swift" \
  -o "${stage_app}/Contents/MacOS/DeployLink"
/bin/cp "${source_root}/Info.plist" "${stage_app}/Contents/Info.plist"
/usr/bin/codesign --force --sign - "${stage_app}" >/dev/null
/usr/bin/codesign --verify --deep --strict "${stage_app}"

if [[ -e "${app_path}" && ! -d "${app_path}" ]]; then
  printf 'Deploy Link install failed: %s exists and is not an app bundle\n' "${app_path}" >&2
  exit 1
fi
/usr/bin/ditto "${stage_app}" "${install_stage}"
/usr/bin/codesign --verify --deep --strict "${install_stage}"
if [[ -d "${app_path}" ]]; then
  /bin/mv "${app_path}" "${install_backup}"
fi
/bin/mv "${install_stage}" "${app_path}"
/usr/bin/codesign --verify --deep --strict "${app_path}"
[[ -x "${executable_path}" ]] || {
  printf 'Deploy Link install failed: installed executable is missing\n' >&2
  exit 1
}
if [[ -d "${install_backup}" ]]; then
  /bin/rm -rf -- "${install_backup}"
fi
install_committed=1

# Retire only the legacy status UI after the replacement app is verified. Device
# bridge LaunchAgents use different labels and remain untouched. A loaded legacy
# job must stop successfully before its backing files are removed.
if /bin/launchctl print "${legacy_launch_target}" >/dev/null 2>&1; then
  /bin/launchctl bootout "${legacy_launch_target}"
fi
/bin/rm -f -- "${legacy_launch_agent_path}"
if [[ -d "${legacy_app_path}" ]]; then
  /bin/rm -rf -- "${legacy_app_path}"
fi

printf 'Installed %s\n' "${app_path}"
