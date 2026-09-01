#!/bin/bash

set -euo pipefail

script_directory="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repository_root="$(cd "${script_directory}/.." && pwd -P)"
source_root="${repository_root}/deploy-link/status-app"
: "${HOME:?HOME must be set}"
runtime_root="${DEPLOY_LINK_RUNTIME_ROOT:-${HOME}/.local/share/deploy-link}"
app_root="${runtime_root}/status"
app_path="${app_root}/Deploy Link Status.app"
executable_path="${app_path}/Contents/MacOS/DeployLinkStatus"
launch_agent_path="${HOME}/Library/LaunchAgents/one.zjz.deploy-link.status.plist"
launch_target="gui/$(id -u)"
stage_root="$(mktemp -d "${runtime_root}/build/.status-app.XXXXXX")"

cleanup() {
  rm -rf "${stage_root}"
}
trap cleanup EXIT

stage_app="${stage_root}/Deploy Link Status.app"
mkdir -p "${stage_app}/Contents/MacOS" "${app_root}" "$(dirname "${launch_agent_path}")"

/usr/bin/xcrun swiftc \
  -O \
  -parse-as-library \
  -target arm64-apple-macos13.0 \
  -framework AppKit \
  -framework SwiftUI \
  "${source_root}/DeployLinkStatusApp.swift" \
  -o "${stage_app}/Contents/MacOS/DeployLinkStatus"
/bin/cp "${source_root}/Info.plist" "${stage_app}/Contents/Info.plist"
/usr/bin/codesign --force --sign - "${stage_app}" >/dev/null
/usr/bin/ditto "${stage_app}" "${app_path}"

/bin/cp "${source_root}/LaunchAgent.plist" "${launch_agent_path}"
/usr/libexec/PlistBuddy -c "Set :ProgramArguments:0 ${executable_path}" "${launch_agent_path}"
/bin/chmod 0600 "${launch_agent_path}"
/bin/launchctl bootout "${launch_target}/one.zjz.deploy-link.status" >/dev/null 2>&1 || true
/bin/launchctl bootstrap "${launch_target}" "${launch_agent_path}"

printf 'Installed %s\n' "${app_path}"
