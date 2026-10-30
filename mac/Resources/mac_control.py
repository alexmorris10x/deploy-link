#!/usr/bin/env python3
"""Private process bridge for the Mac app. Never returns credentials or device records."""
import argparse
import json
import os
from pathlib import Path
import plistlib
import subprocess
import sys
import tempfile
import urllib.request
from urllib.parse import urlparse

LABEL = 'com.10x.deploy-link-ota'
PLIST = Path.home() / 'Library/LaunchAgents' / (LABEL + '.plist')
RUNTIME = Path.home() / '.local/share/deploy-link'
RESOURCES = Path(__file__).resolve().parent


def run(args, timeout=30, check=True):
    result = subprocess.run(args, capture_output=True, text=True, timeout=timeout)
    if check and result.returncode:
        # Command output is never relayed: installer paths and external tools may
        # include private material. The app reports the failed action instead.
        raise RuntimeError('The operation did not complete. Check Xcode and Tailscale setup, then try again.')
    return result


def service_config():
    if not PLIST.exists():
        return None
    if PLIST.is_symlink() or PLIST.stat().st_uid != os.getuid():
        raise RuntimeError('The queue service configuration must belong to your Mac login.')
    with PLIST.open('rb') as source:
        data = plistlib.load(source)
    args = data.get('ProgramArguments', [])
    if (data.get('Label') != LABEL or len(args) < 3 or 'serve' not in args
            or Path(args[1]).name != 'ota-queue'):
        raise RuntimeError('The installed queue configuration is not compatible with this app.')
    def value(flag, default=None):
        try:
            return args[args.index(flag) + 1]
        except (ValueError, IndexError):
            return default
    url = value('--base-url', '')
    parsed = urlparse(url)
    port = int(value('--port', '8477'))
    if (parsed.scheme != 'https' or not parsed.hostname or parsed.username is not None
            or parsed.password is not None or parsed.query or parsed.fragment
            or parsed.path not in ('', '/') or parsed.port not in (None, 443)
            or not 1024 <= port <= 65535):
        raise RuntimeError('The queue service needs a valid private HTTPS root address and local port.')
    return {'python': args[0], 'script': args[1],
            'root': value('--root', str(Path.home() / '.local/share/ios-ota/queue')),
            'url': url, 'port': port,
            'login': bool(data.get('RunAtLoad', False)), 'plist': data}


def prerequisites():
    xcode = run(['/usr/bin/xcode-select', '-p'], check=False)
    full_xcode = xcode.returncode == 0 and '.app/Contents/Developer' in xcode.stdout
    candidates = ['/Applications/Tailscale.app/Contents/MacOS/Tailscale',
                  str(Path.home() / 'Applications/Tailscale.app/Contents/MacOS/Tailscale'),
                  '/opt/homebrew/bin/tailscale', '/usr/local/bin/tailscale']
    tailscale = next((p for p in candidates if os.access(p, os.X_OK)), None)
    connected = False
    if tailscale:
        result = run([tailscale, 'status', '--json'], timeout=10, check=False)
        try:
            connected = json.loads(result.stdout).get('BackendState') == 'Running'
        except (ValueError, AttributeError):
            pass
    return {'xcode': full_xcode, 'tailscale': bool(tailscale), 'connected': connected}


def loaded():
    return run(['/bin/launchctl', 'print', f'gui/{os.getuid()}/{LABEL}'], check=False).returncode == 0


def status():
    config = service_config()
    result = {'configured': config is not None, 'running': False, 'queueURL': '',
              'queueAtLogin': False, 'releases': [], **prerequisites()}
    if config:
        result['queueURL'] = config['url']
        result['queueAtLogin'] = config['login']
        try:
            with urllib.request.urlopen(f'http://127.0.0.1:{config["port"]}/api/releases', timeout=3) as response:
                body = json.load(response)
            releases = body.get('releases', [])
            result['running'] = loaded()
            result['releases'] = [{key: str(item.get(key, '')) for key in
                                   ('id', 'title', 'version', 'build', 'note')} for item in releases]
        except (OSError, ValueError, AttributeError):
            pass
    return result


def start():
    if service_config() is None:
        raise RuntimeError('Choose Set Up Server first.')
    domain = f'gui/{os.getuid()}'
    run(['/bin/launchctl', 'enable', f'{domain}/{LABEL}'])
    if not loaded():
        run(['/bin/launchctl', 'bootstrap', domain, str(PLIST)])
    run(['/bin/launchctl', 'kickstart', f'{domain}/{LABEL}'])


def stop():
    if service_config() is not None and loaded():
        run(['/bin/launchctl', 'bootout', f'gui/{os.getuid()}/{LABEL}'])


def login(enabled):
    config = service_config()
    if config is None:
        raise RuntimeError('Choose Set Up Server first.')
    was_running = status()['running']
    data = config['plist']
    data['RunAtLoad'] = enabled
    data['KeepAlive'] = enabled
    with tempfile.NamedTemporaryFile(dir=PLIST.parent, prefix=LABEL + '.', delete=False) as output:
        temporary = Path(output.name)
        plistlib.dump(data, output)
    temporary.chmod(0o600)
    temporary.replace(PLIST)
    stop()
    if was_running:
        start()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('action', choices=['status', 'setup', 'start', 'stop', 'login', 'publish', 'copy-management'])
    parser.add_argument('--source')
    parser.add_argument('--note', default='')
    parser.add_argument('--enabled', choices=['yes', 'no'])
    args = parser.parse_args()
    if args.action == 'setup':
        checks = prerequisites()
        if not checks['xcode']:
            raise RuntimeError('Install full Xcode, open it once, and select its command-line tools in Xcode Settings → Locations.')
        if not checks['connected']:
            raise RuntimeError('Open Tailscale, sign in, and connect this Mac before setup.')
        run(['/bin/bash', str(RESOURCES / 'scripts/install_ota_queue.sh')], timeout=120)
    elif args.action == 'start':
        start()
    elif args.action == 'stop':
        stop()
    elif args.action == 'login':
        login(args.enabled == 'yes')
    elif args.action in ('publish', 'copy-management'):
        config = service_config()
        if config is None:
            raise RuntimeError('Choose Set Up Server first.')
        command = [config['python'], config['script'], '--root', config['root']]
        if args.action == 'publish':
            source = Path(args.source or '').resolve()
            if source.suffix.lower() not in ('.app', '.ipa') or not source.exists():
                raise RuntimeError('Choose an existing signed iOS .app or .ipa file.')
            command += ['publish', str(source), '--note', args.note]
        else:
            command += ['admin-token', '--copy']
        run(command, timeout=180)
    print(json.dumps(status()))


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        message = str(error) if isinstance(error, RuntimeError) else 'The queue operation failed. Check setup and try again.'
        print(json.dumps({'error': message}))
        sys.exit(1)
