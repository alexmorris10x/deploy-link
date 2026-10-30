import importlib.util
import json
from pathlib import Path
import plistlib
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('mac_control', Path(__file__).parent / 'Resources/mac_control.py')
control = importlib.util.module_from_spec(spec)
spec.loader.exec_module(control)


class MacServiceTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.plist = self.root / 'service.plist'
        self.queue = self.root / 'existing queue'
        self.queue.mkdir()
        self.data = {'Label': control.LABEL, 'ProgramArguments': [
            '/usr/bin/python3', str(self.root / 'ota-queue'), '--root', str(self.queue),
            'serve', '--base-url', 'https://example.test:8443/', '--port', '8478'],
            'RunAtLoad': True, 'KeepAlive': True, 'CustomSetting': 'preserved'}
        self.plist.write_bytes(plistlib.dumps(self.data))
        self.override = patch.object(control, 'PLIST', self.plist)
        self.override.start()
        self.addCleanup(self.override.stop)

    def test_adopts_custom_existing_queue_without_reinitializing(self):
        existing = self.queue / 'retained-build'
        existing.write_text('retained')
        config = control.service_config()
        self.assertEqual(config['root'], str(self.queue))
        self.assertEqual(config['port'], 8478)
        self.assertEqual(existing.read_text(), 'retained')

    def test_rejects_different_service_before_actions(self):
        self.data['Label'] = 'another.service'
        self.plist.write_bytes(plistlib.dumps(self.data))
        with self.assertRaises(RuntimeError):
            control.service_config()

    def test_does_not_expose_credential_url(self):
        self.data['ProgramArguments'][6] = 'https://user:private@example.test/'
        self.plist.write_bytes(plistlib.dumps(self.data))
        with self.assertRaises(RuntimeError) as failure:
            control.service_config()
        self.assertNotIn('private', str(failure.exception).replace('private HTTPS', 'HTTPS'))

    def test_login_change_preserves_configuration_and_stopped_state(self):
        with patch.object(control, 'status', return_value={'running': False}), \
             patch.object(control, 'stop') as stop, patch.object(control, 'start') as start:
            control.login(False)
        result = plistlib.loads(self.plist.read_bytes())
        self.assertEqual(result['ProgramArguments'], self.data['ProgramArguments'])
        self.assertEqual(result['CustomSetting'], 'preserved')
        self.assertFalse(result['KeepAlive'])
        self.assertFalse(result['RunAtLoad'])
        stop.assert_called_once()
        start.assert_not_called()
        self.assertEqual(self.plist.stat().st_mode & 0o777, 0o600)

    def test_login_change_keeps_running_queue_running(self):
        with patch.object(control, 'status', return_value={'running': True}), \
             patch.object(control, 'stop') as stop, patch.object(control, 'start') as start:
            control.login(False)
        stop.assert_called_once()
        start.assert_called_once()

    def test_stop_only_unloads_our_service_and_preserves_queue(self):
        retained = self.queue / 'existing-build'
        retained.write_text('build')
        with patch.object(control, 'loaded', return_value=True), patch.object(control, 'run') as run:
            control.stop()
        self.assertEqual(run.call_args.args[0], ['/bin/launchctl', 'bootout', f'gui/{control.os.getuid()}/{control.LABEL}'])
        self.assertEqual(retained.read_text(), 'build')
        self.assertTrue(self.plist.exists())

    def test_external_error_output_is_not_relayed(self):
        result = subprocess.CompletedProcess([], 1, stdout='private output', stderr='private error')
        with patch.object(control.subprocess, 'run', return_value=result):
            with self.assertRaises(RuntimeError) as failure:
                control.run(['/bin/false'])
        self.assertNotIn('private', str(failure.exception))


if __name__ == '__main__':
    unittest.main()
