#!/bin/bash
# Install or update this user's private Deploy Link queue. No queue contents,
# signing settings, other LaunchAgents, or existing Tailscale routes are reset.

set +x
set -euo pipefail
umask 077

fail() { printf '%s\n' "$*" >&2; exit 1; }
[[ "$(/usr/bin/uname -s)" == Darwin ]] || fail 'Deploy Link server setup requires macOS.'
[[ "$(id -u)" != 0 ]] || fail 'Run this installer as your normal Mac login user, without sudo.'
: "${HOME:?HOME must be set}"
script_directory="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repository_root="$(cd "${script_directory}/.." && pwd -P)"
runtime_root="${DEPLOY_LINK_RUNTIME_ROOT:-${HOME}/.local/share/deploy-link}"
queue_root="${DEPLOY_LINK_QUEUE_ROOT:-${HOME}/.local/share/ios-ota/queue}"
installed_script="${runtime_root}/bin/ota-queue"
log_path="${runtime_root}/logs/ota-queue.log"
label="com.10x.deploy-link-ota"
plist_path="${HOME}/Library/LaunchAgents/${label}.plist"
port="${DEPLOY_LINK_OTA_PORT:-8477}"

/usr/bin/xcode-select -p >/dev/null 2>&1 || fail 'Install Xcode and select its developer tools before setup. Build and sign your iOS apps with Xcode.'
/usr/bin/xcrun --find xcodebuild >/dev/null 2>&1 || fail 'Full Xcode is required for signing development or ad hoc iOS builds; select Xcode in its Locations settings.'

python="${DEPLOY_LINK_PYTHON:-}"
if [[ -n "${python}" ]]; then
  [[ -x "${python}" ]] || fail 'DEPLOY_LINK_PYTHON must name an executable Python 3.9 or later.'
else
  for candidate in "$(command -v python3 || true)" /opt/homebrew/bin/python3 /usr/local/bin/python3 /usr/bin/python3; do
    if [[ -n "${candidate}" && -x "${candidate}" ]] && "${candidate}" -c 'import sys; sys.exit(sys.version_info < (3, 9))' 2>/dev/null; then
      python="${candidate}"
      break
    fi
  done
fi
[[ -n "${python}" ]] || fail 'Install Python 3.9 or later from python.org, then run this installer again.'
"${python}" -c 'import sys; sys.exit(sys.version_info < (3, 9))' || fail 'Python 3.9 or later is required.'

# Save the absolute interpreter selected by this installation for launchd.
python="$("${python}" -c 'import sys; print(sys.executable)')"
tailscale="${DEPLOY_LINK_TAILSCALE:-}"
if [[ -n "${tailscale}" ]]; then
  [[ -x "${tailscale}" ]] || fail 'DEPLOY_LINK_TAILSCALE must name the Tailscale executable.'
else
  for candidate in /Applications/Tailscale.app/Contents/MacOS/Tailscale "${HOME}/Applications/Tailscale.app/Contents/MacOS/Tailscale" /opt/homebrew/bin/tailscale /usr/local/bin/tailscale; do
    if [[ -x "${candidate}" ]]; then tailscale="${candidate}"; break; fi
  done
fi
[[ -n "${tailscale}" ]] || fail 'Install Tailscale from tailscale.com/download/mac, sign in, and connect this Mac first.'

dns_name="$("${tailscale}" status --json | "${python}" -c '
import json, sys
state = json.load(sys.stdin)
name = (state.get("Self") or {}).get("DNSName", "").rstrip(".")
if state.get("BackendState") != "Running" or not name:
    sys.exit("Open Tailscale, sign in, and connect this Mac before setup.")
print(name)
')"
base_url="${DEPLOY_LINK_OTA_BASE_URL:-https://${dns_name}/}"
# Base URLs refer only to this Mac; a typo must not publish links to another host.
"${python}" - "${base_url}" "${dns_name}" "${port}" <<'PY'
import sys
from urllib.parse import urlparse
url = urlparse(sys.argv[1])
if (url.scheme != 'https' or url.hostname != sys.argv[2] or url.port not in (None, 443)
        or url.path not in ('', '/') or url.query or url.fragment or url.username is not None or url.password is not None):
    sys.exit('DEPLOY_LINK_OTA_BASE_URL must be this Mac\'s root Tailscale HTTPS URL.')
try:
    valid_port = 1024 <= int(sys.argv[3]) <= 65535
except ValueError:
    valid_port = False
if not valid_port:
    sys.exit('DEPLOY_LINK_OTA_PORT must be between 1024 and 65535.')
PY

# This is deliberately before installing files or stopping any existing service.
# Adding our root leaves unrelated /path handlers intact. Conflicts fail closed.
serve_state="$("${tailscale}" serve status --json | "${python}" "${repository_root}/ota/ota_queue.py" check-serve --base-url "${base_url}" --port "${port}")"

# Refuse symlinked runtime/queue locations rather than following them with chmod.
"${python}" - "${runtime_root}" "${queue_root}" "$(dirname "${plist_path}")" <<'PY'
import os, pathlib, sys
for value in sys.argv[1:]:
    path = pathlib.Path(value)
    if not path.is_absolute() or path.is_symlink():
        sys.exit('Runtime, queue, and LaunchAgents paths must be absolute directories, not symlinks.')
    path.mkdir(mode=0o700, parents=True, exist_ok=True)
    if path.stat().st_uid != os.getuid():
        sys.exit('Setup directories must belong to the current Mac user.')
for value in sys.argv[1:3]:
    pathlib.Path(value).chmod(0o700)
PY
/bin/mkdir -p "${runtime_root}/bin" "$(dirname "${log_path}")"
/bin/chmod 700 "${runtime_root}/bin" "$(dirname "${log_path}")"
# Generate once; keep the existing capability on updates. This command never displays it.
"${python}" "${repository_root}/ota/ota_queue.py" --root "${queue_root}" admin-token >/dev/null

# plistlib escapes spaces, ampersands, and other characters in user-selected paths.
"${python}" - "${plist_path}.tmp" "${label}" "${python}" "${installed_script}" "${queue_root}" "${base_url}" "${port}" "${log_path}" <<'PY'
import os, plistlib, sys
path, label, python, script, queue, url, port, log = sys.argv[1:]
plist = {
    'Label': label,
    'ProgramArguments': [python, script, '--root', queue, 'serve', '--base-url', url, '--port', port],
    'RunAtLoad': True, 'KeepAlive': True, 'ThrottleInterval': 10,
    'StandardOutPath': log, 'StandardErrorPath': log, 'Umask': 0o077,
}
with open(path, 'wb') as output:
    plistlib.dump(plist, output)
os.chmod(path, 0o600)
PY
/usr/bin/plutil -lint "${plist_path}.tmp" >/dev/null
/usr/bin/install -m 755 "${repository_root}/ota/ota_queue.py" "${installed_script}.tmp"

# Configure only a missing endpoint; never reset Serve or touch Funnel.
if [[ "${serve_state}" == configure ]]; then
  "${tailscale}" serve --bg --https=443 --set-path=/ "http://127.0.0.1:${port}"
fi
# A concurrent route change must not be mistaken for our endpoint.
confirmed_state="$("${tailscale}" serve status --json | "${python}" "${repository_root}/ota/ota_queue.py" check-serve --base-url "${base_url}" --port "${port}")"
[[ "${confirmed_state}" == existing ]] || fail 'Tailscale HTTPS setup is not ready. Enable HTTPS for this tailnet and run setup again.'

/bin/mv -f "${installed_script}.tmp" "${installed_script}"
/bin/mv -f "${plist_path}.tmp" "${plist_path}"
/usr/bin/touch "${log_path}"
/bin/chmod 600 "${log_path}"
domain="gui/$(id -u)"
/bin/launchctl bootout "${domain}/${label}" 2>/dev/null || true
/bin/launchctl bootstrap "${domain}" "${plist_path}"

for _ in 1 2 3 4 5 6 7 8 9 10; do
  if /usr/bin/curl -fsS "http://127.0.0.1:${port}/api/releases" >/dev/null 2>&1; then
    printf 'Deploy Link queue serving %s (log %s)\n' "${base_url}" "${log_path}"
    printf 'Copy management access locally (the token is never printed):\n  '
    printf '%q ' "${python}" "${installed_script}" --root "${queue_root}" admin-token --copy
    printf '\n'
    exit 0
  fi
  /bin/sleep 1
done
printf 'Deploy Link did not answer on port %s; see %s\n' "${port}" "${log_path}" >&2
exit 1
