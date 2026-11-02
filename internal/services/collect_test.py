import contextlib
import io
import json
from pathlib import Path
import runpy
import subprocess
import unittest
from unittest.mock import patch

SCRIPT = Path(__file__).with_name('collect.py')

class CollectorTests(unittest.TestCase):
    def execute(self, which, run):
        out = io.StringIO()
        with patch('shutil.which', side_effect=which), patch('subprocess.run', side_effect=run), contextlib.redirect_stdout(out):
            runpy.run_path(str(SCRIPT))
        return json.loads(out.getvalue())

    def test_actual_fields_and_no_secrets(self):
        def run(command, **kwargs):
            self.assertEqual(kwargs['timeout'], 6)
            if command[0] == 'systemctl':
                text = 'nginx.service loaded active running Web server\npostgres.service loaded failed failed Database\n'
            elif command[0] == 'docker':
                # Command/Env must not be requested from Docker at all.
                self.assertNotIn('.Command', command[-1])
                self.assertNotIn('.Env', command[-1])
                text = json.dumps(dict(Names='web', State='running', Status='Up 1 hour', Image='nginx:alpine', Ports='8080->80/tcp'))
            else:
                text = 'tcp LISTEN 0 128 0.0.0.0:80 0.0.0.0:* users:(("nginx",pid=32,fd=6))\nudp UNCONN 0 0 0.0.0.0:53 0.0.0.0:*\n'
            return subprocess.CompletedProcess(command, 0, text)
        result = self.execute(lambda cmd: '/usr/bin/' + cmd, run)
        self.assertEqual(len(result['items']), 5)
        self.assertEqual(result['items'][0]['name'], 'nginx.service')
        self.assertEqual(result['items'][1]['state'], 'failed')
        self.assertEqual(result['items'][3]['name'], 'nginx')
        self.assertEqual(result['items'][4]['name'], '进程不可见')

    def test_permission_timeout_and_missing_are_not_empty_success(self):
        def run(command, **kwargs):
            if command[0] == 'systemctl':
                raise subprocess.TimeoutExpired(command, 6)
            return subprocess.CompletedProcess(command, 1, '')
        result = self.execute(lambda cmd: None if cmd == 'docker' else cmd, run)
        self.assertEqual(result['items'], [])
        self.assertEqual(result['sources']['systemd']['status'], 'error')
        self.assertEqual(result['sources']['docker']['status'], 'unavailable')
        self.assertEqual(result['sources']['ports']['status'], 'error')

    def test_limits_are_explicit(self):
        result = self.execute(lambda cmd: cmd if cmd == 'systemctl' else None,
                              lambda command, **kwargs: subprocess.CompletedProcess(command, 0, 'app.service loaded active running App\n' * 600))
        self.assertEqual(len(result['items']), 500)
        self.assertTrue(result['sources']['systemd']['truncated'])

if __name__ == '__main__':
    unittest.main()
