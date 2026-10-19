#!/bin/bash
# Install the OTA queue server as a login LaunchAgent and make sure Tailscale
# serves it to the tailnet. Safe to rerun: it replaces the installed script and
# restarts only this one agent.

set -euo pipefail

script_directory="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repository_root="$(cd "${script_directory}/.." && pwd -P)"
: "${HOME:?HOME must be set}"
runtime_root="${DEPLOY_LINK_RUNTIME_ROOT:-${HOME}/.local/share/deploy-link}"
installed_script="${runtime_root}/bin/ota-queue"
queue_root="${HOME}/.local/share/ios-ota/queue"
log_path="${runtime_root}/profiles/logs/ota-queue.log"
label="com.10x.deploy-link-ota"
plist_path="${HOME}/Library/LaunchAgents/${label}.plist"
port="${DEPLOY_LINK_OTA_PORT:-8477}"
tailscale="/Applications/Tailscale.app/Contents/MacOS/Tailscale"

if [[ -z "${DEPLOY_LINK_OTA_BASE_URL:-}" ]]; then
  dns_name="$("${tailscale}" status --json | /usr/bin/python3 -c 'import json, sys; print(json.load(sys.stdin)["Self"]["DNSName"].rstrip("."))')"
  DEPLOY_LINK_OTA_BASE_URL="https://${dns_name}/"
fi

/bin/mkdir -p "${runtime_root}/bin" "$(dirname "${log_path}")" "${queue_root}" "$(dirname "${plist_path}")"
/bin/chmod 700 "${queue_root}"
/usr/bin/install -m 755 "${repository_root}/ota/ota_queue.py" "${installed_script}.tmp"
/bin/mv -f "${installed_script}.tmp" "${installed_script}"

/bin/cat > "${plist_path}.tmp" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>${label}</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/bin/python3</string>
    <string>${installed_script}</string>
    <string>serve</string>
    <string>--base-url</string><string>${DEPLOY_LINK_OTA_BASE_URL}</string>
    <string>--port</string><string>${port}</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>10</integer>
  <key>StandardOutPath</key><string>${log_path}</string>
  <key>StandardErrorPath</key><string>${log_path}</string>
</dict>
</plist>
PLIST
/usr/bin/plutil -lint "${plist_path}.tmp" >/dev/null
/bin/mv -f "${plist_path}.tmp" "${plist_path}"

domain="gui/$(id -u)"
/bin/launchctl bootout "${domain}/${label}" 2>/dev/null || true
/bin/launchctl bootstrap "${domain}" "${plist_path}"

# Tailscale serve proxies the tailnet HTTPS address to the loopback port.
if ! "${tailscale}" serve status 2>/dev/null | /usr/bin/grep -q "127.0.0.1:${port}"; then
  "${tailscale}" serve --bg --https=443 "http://127.0.0.1:${port}"
fi

for _ in 1 2 3 4 5 6 7 8 9 10; do
  if /usr/bin/curl -fsS "http://127.0.0.1:${port}/api/releases" >/dev/null 2>&1; then
    printf 'OTA queue serving %s (log %s)\n' "${DEPLOY_LINK_OTA_BASE_URL}" "${log_path}"
    exit 0
  fi
  /bin/sleep 1
done
printf 'OTA queue did not answer on port %s; see %s\n' "${port}" "${log_path}" >&2
exit 1
