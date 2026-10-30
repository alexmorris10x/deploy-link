#!/bin/bash
# Build locally, sign with Developer ID, notarize, and package. Never publishes.
set -euo pipefail
umask 077

usage() {
  cat <<'USAGE'
Usage: scripts/package-mac-release.sh [--check]

Required environment (names only; never put passwords in this script):
  DEPLOY_LINK_DEVELOPER_ID    Full Developer ID Application identity name
  DEPLOY_LINK_NOTARY_PROFILE Existing notarytool Keychain profile name

Optional environment:
  DEPLOY_LINK_BUILD_WRAPPER  Defaults to ~/bin/ios-dev-build
  DEPLOY_LINK_LANE_COMMAND   Defaults to ~/bin/lane
  DEPLOY_LINK_RELEASE_ROOT   Defaults to ~/Developer/builds/agent-scratch/deploy-link-mac-release

--check validates local prerequisites without building or uploading.
The normal run uploads only to Apple's notarization service. It does not
upload to GitHub, publish a release, or submit an app to the App Store.
USAGE
}

die() { printf '%s\n' "$*" >&2; exit 1; }
check_only=0
case "${1:-}" in
  --check) check_only=1; shift ;;
  -h|--help) usage; exit 0 ;;
  '') ;;
  *) usage >&2; exit 64 ;;
esac
[[ $# -eq 0 ]] || { usage >&2; exit 64; }
[[ "$(uname -s)" == Darwin ]] || die 'Mac release packaging requires macOS.'
identity="${DEPLOY_LINK_DEVELOPER_ID:-}"
profile="${DEPLOY_LINK_NOTARY_PROFILE:-}"
[[ "$identity" == 'Developer ID Application: '* ]] || die 'Set DEPLOY_LINK_DEVELOPER_ID to a Developer ID Application identity. Apple Development/Distribution identities are not suitable.'
[[ -n "$profile" ]] || die 'Set DEPLOY_LINK_NOTARY_PROFILE to an existing notarytool Keychain profile.'
wrapper="${DEPLOY_LINK_BUILD_WRAPPER:-$HOME/bin/ios-dev-build}"
lane_command="${DEPLOY_LINK_LANE_COMMAND:-$HOME/bin/lane}"
[[ -x "$wrapper" ]] || die 'The ios-dev-build wrapper is unavailable; no direct xcodebuild fallback is used.'
[[ -x "$lane_command" ]] || die 'The lane capacity check is unavailable.'
for tool in xcodegen python3 codesign security ditto hdiutil xcrun shasum spctl lipo; do
  command -v "$tool" >/dev/null || die "Missing required tool: $tool"
done
security find-identity -v -p codesigning | IDENTITY_TO_CHECK="$identity" python3 -c '
import os, sys
expected = "\"" + os.environ["IDENTITY_TO_CHECK"] + "\""
if not any(expected in line for line in sys.stdin):
    sys.exit("Requested Developer ID Application identity and private key are not available in the Keychain.")
'
xcrun --find notarytool >/dev/null
xcrun --find stapler >/dev/null
repo_root="$(cd "$(dirname "$0")/.." && pwd)"
release_root="${DEPLOY_LINK_RELEASE_ROOT:-$HOME/Developer/builds/agent-scratch/deploy-link-mac-release}"
release_root="$(python3 - "$release_root" <<'PY'
from pathlib import Path
import sys
target = Path(sys.argv[1]).expanduser().resolve()
builds = (Path.home() / 'Developer/builds').resolve()
if not target.is_relative_to(builds) or target == builds:
    sys.exit('Release output must be a task directory under ~/Developer/builds on the internal disk.')
print(target)
PY
)"
[[ -f "$repo_root/mac/project.yml" ]] || die 'The native Mac project is missing.'
if [[ "$check_only" -eq 1 ]]; then
  printf '%s\n' 'Local prerequisites passed. Notary profile authentication has not been contacted or checked.'
  exit 0
fi

capacity="$("$lane_command" build --host mac --needs-gb 3)"
printf '%s\n' "$capacity"
[[ "$capacity" == *'LANE_HOST=mac'* ]] || die 'Mac capacity check did not authorize this build.'
mkdir -p "$release_root"
job_root="$(mktemp -d "$release_root/package.XXXXXX")"
archive="$job_root/DeployLinkMac.xcarchive"
stage="$job_root/disk-image"
mkdir -p "$stage"
cd "$repo_root/mac"
xcodegen generate
"$wrapper" deploy-link public-mac-release --owner codex --task deploy-link-mac-release -- \
  -project DeployLinkMac.xcodeproj -scheme DeployLinkMac -configuration Release \
  -destination 'generic/platform=macOS' -archivePath "$archive" \
  CODE_SIGNING_ALLOWED=NO CODE_SIGNING_REQUIRED=NO CODE_SIGN_IDENTITY= \
  ENABLE_HARDENED_RUNTIME=YES ONLY_ACTIVE_ARCH=NO 'ARCHS=arm64 x86_64' archive
source_app="$archive/Products/Applications/Deploy Link.app"
[[ -d "$source_app" ]] || die "Expected archive product missing: $source_app"
app="$stage/Deploy Link.app"
ditto "$source_app" "$app"
cp "$repo_root/LICENSE" "$repo_root/NOTICE" "$stage/"
ln -s /Applications "$stage/Applications"

# Deploy Link has no private executable entitlements. Sign actual nested code
# inside-out rather than using --deep to sign an unknown bundle hierarchy.
python3 - "$app" "$identity" "$job_root" <<'PY'
from pathlib import Path
import plistlib, subprocess, sys
app, identity, scratch = Path(sys.argv[1]), sys.argv[2], Path(sys.argv[3])
info = plistlib.loads((app / 'Contents/Info.plist').read_bytes())
if info.get('CFBundleIdentifier') != 'com.10x.deploylink.mac':
    sys.exit('Refusing an unexpected Mac bundle identity.')
executable = app / 'Contents/MacOS' / info['CFBundleExecutable']
architectures = subprocess.check_output(['lipo', '-archs', str(executable)], text=True).split()
if not {'arm64', 'x86_64'}.issubset(architectures):
    sys.exit('The release must support both Apple silicon and Intel Macs.')
empty = scratch / 'release-entitlements.plist'
empty.write_bytes(plistlib.dumps({}))
magic = {bytes.fromhex(x) for x in ('feedface','feedfacf','cefaedfe','cffaedfe','cafebabe','bebafeca','cafebabf','bfbafeca')}
bundle_types = {'.app', '.xpc', '.appex', '.framework', '.bundle'}
code = []
for path in app.rglob('*'):
    if path.is_symlink():
        continue
    if path.name == 'embedded.provisionprofile':
        path.unlink()  # A direct Developer ID distribution has no dev profile.
    elif path.is_dir() and path.suffix in bundle_types:
        code.append(path)
    elif path.is_file():
        with path.open('rb') as handle:
            if handle.read(4) in magic:
                code.append(path)
code.sort(key=lambda path: len(path.parts), reverse=True)
for path in code + [app]:
    subprocess.run(['codesign','--force','--options','runtime','--timestamp=http://timestamp.apple.com/ts01',
                    '--entitlements',str(empty),'--sign',identity,str(path)], check=True)
subprocess.run(['codesign','--verify','--deep','--strict','--verbose=2',str(app)], check=True)
for path in code + [app]:
    result = subprocess.run(['codesign','-d','--entitlements',':-',str(path)],
                            capture_output=True, check=True)
    entitlements = plistlib.loads(result.stdout) if result.stdout.strip() else {}
    if entitlements.get('com.apple.security.get-task-allow') or entitlements.get('get-task-allow'):
        sys.exit(f'Debug entitlement found: {path.name}')
    details = subprocess.run(['codesign','-dv','--verbose=4',str(path)],
                             capture_output=True, text=True, check=True).stderr
    if 'Authority=Developer ID Application:' not in details or 'Timestamp=' not in details or 'runtime' not in details:
        sys.exit(f'Missing Developer ID, timestamp, or hardened runtime: {path.name}')
PY

notarize() {
  local target="$1"
  local result="$2"
  xcrun notarytool submit "$target" --keychain-profile "$profile" --wait --output-format json > "$result"
  local submission_id
  submission_id="$(python3 - "$result" <<'PY'
import json, sys
result = json.load(open(sys.argv[1]))
if result.get('status') != 'Accepted':
    sys.exit('Apple did not accept the notarization. Inspect the result in the build directory; do not distribute.')
print(result['id'])
PY
)"
  xcrun notarytool log "$submission_id" --keychain-profile "$profile" "$result.log.json"
  python3 - "$result.log.json" <<'PY'
import json, sys
data = json.load(open(sys.argv[1]))
issues = data.get('issues') or []
if issues:
    sys.exit('Notarization returned issues. Inspect its log before distributing this release.')
PY
}

ditto -c -k --keepParent "$app" "$job_root/DeployLinkMac-for-notary.zip"
notarize "$job_root/DeployLinkMac-for-notary.zip" "$job_root/app-notary.json"
xcrun stapler staple "$app"
xcrun stapler validate "$app"
codesign --verify --deep --strict --verbose=2 "$app"
spctl --assess --type execute --verbose=2 "$app"

version="$(/usr/libexec/PlistBuddy -c 'Print CFBundleShortVersionString' "$app/Contents/Info.plist")"
build="$(/usr/libexec/PlistBuddy -c 'Print CFBundleVersion' "$app/Contents/Info.plist")"
[[ "$version" =~ ^[0-9]+(\.[0-9]+)*$ && "$build" =~ ^[0-9]+$ ]] || die 'Unexpected version or build value.'
dmg="$job_root/Deploy-Link-Mac-$version-$build.dmg"
hdiutil create -volname 'Deploy Link' -srcfolder "$stage" -ov -format UDZO "$dmg"
codesign --force --timestamp=http://timestamp.apple.com/ts01 --sign "$identity" "$dmg"
notarize "$dmg" "$job_root/dmg-notary.json"
xcrun stapler staple "$dmg"
xcrun stapler validate "$dmg"
codesign --verify --strict --verbose=2 "$dmg"
hdiutil verify "$dmg"
spctl --assess --type open --context context:primary-signature --verbose=2 "$dmg"
(cd "$job_root" && shasum -a 256 "$(basename "$dmg")" > "$(basename "$dmg").sha256")
printf '\nReady for release upload:\n%s\n%s\n' "$dmg" "$dmg.sha256"
