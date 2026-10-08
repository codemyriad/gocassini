"""Exercise APT recovery without root, network access, or host APT changes."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


SCRIPT = Path(__file__).with_name('ci-apt-update.sh')
MIRROR = 'mirror+file:/etc/apt/apt-mirrors.txt'


class AptRecoveryTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.sources = self.root / 'sources'
        self.sources.mkdir()
        self.bin = self.root / 'bin'
        self.bin.mkdir()
        self.env = dict(os.environ, PATH=f'{self.bin}:{os.environ["PATH"]}',
                        APT_SOURCES_DIR=str(self.sources),
                        APT_MAIN_SOURCES=str(self.root / 'sources.list'),
                        APT_CONFIG_DIR=str(self.root / 'config'),
                        APT_UPDATE_DEADLINE='0.2s', TEST_ROOT=str(self.root))
        self.stub('dpkg', 'echo "${TEST_ARCH:-amd64}"')
        self.stub('apt-get', '''
echo "$*" >> "$TEST_ROOT/calls"
cp "$APT_SOURCES_DIR/ubuntu.sources" "$TEST_ROOT/observed-$(wc -l < "$TEST_ROOT/calls")"
case "${TEST_MODE:-recover}" in
  success) exit 0 ;;
  fail) exit 100 ;;
esac
if [ "$(wc -l < "$TEST_ROOT/calls")" -eq 1 ]; then
  if [ "${TEST_MODE:-}" = hang ]; then sleep 30; fi
  exit 100
fi
exit 0
''')
        self.original = (f'Types: deb\nURIs: {MIRROR}\n'
                         'Suites: noble noble-updates noble-security\n'
                         'Components: main universe\nSigned-By: /ubuntu-key.gpg\n')
        self.source = self.sources / 'ubuntu.sources'
        self.source.write_text(self.original)

    def stub(self, name, body):
        path = self.bin / name
        path.write_text('#!/bin/sh\nset -eu\n' + body + '\n')
        path.chmod(0o755)

    def run_update(self, **env):
        return subprocess.run(['bash', str(SCRIPT)], env=dict(self.env, **env),
                              capture_output=True, text=True, timeout=10)

    def calls(self):
        return (self.root / 'calls').read_text().splitlines()

    def test_success_preserves_mirrors_and_sets_install_limits(self):
        result = self.run_update(TEST_MODE='success')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.source.read_text(), self.original)
        self.assertEqual(len(self.calls()), 1)
        config = (self.root / 'config/99-cassini-network-limits').read_text()
        self.assertIn('Acquire::http::Timeout "30";', config)
        self.assertIn('Acquire::https::Timeout "30";', config)
        self.assertIn('Acquire::Retries "2";', config)
        self.assertIn('APT::Update::Error-Mode=any', self.calls()[0])

    def test_failure_recovers_once_and_preserves_source_metadata(self):
        result = self.run_update()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(self.calls()), 2)
        self.assertEqual((self.root / 'observed-1').read_text(), self.original)
        self.assertEqual(self.source.read_text(), self.original.replace(
            MIRROR, 'https://archive.ubuntu.com/ubuntu/'))

    def test_hanging_update_is_terminated_and_recovers(self):
        result = self.run_update(TEST_MODE='hang')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('status 124', result.stderr)
        self.assertEqual(len(self.calls()), 2)

    def test_recovery_failure_propagates(self):
        result = self.run_update(TEST_MODE='fail')
        self.assertEqual(result.returncode, 100)
        self.assertEqual(len(self.calls()), 2)

    def test_arm_does_not_rewrite_or_retry(self):
        result = self.run_update(TEST_ARCH='arm64')
        self.assertEqual(result.returncode, 100)
        self.assertEqual(self.source.read_text(), self.original)
        self.assertEqual(len(self.calls()), 1)

    def test_legacy_sources_and_ports(self):
        legacy = self.root / 'sources.list'
        legacy.write_text('deb [arch=amd64] http://azure.archive.ubuntu.com/ubuntu noble main\n')
        ports = self.sources / 'ports.list'
        ports.write_text('deb https://ports.ubuntu.com/ubuntu-ports noble main\n')
        before = ports.read_text()
        result = self.run_update()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(legacy.read_text(), 'deb [arch=amd64] https://archive.ubuntu.com/ubuntu/ noble main\n')
        self.assertEqual(ports.read_text(), before)

    def test_unknown_mirror_list_is_not_rewritten(self):
        original = self.original.replace(MIRROR, MIRROR + '.other')
        self.source.write_text(original)
        result = self.run_update()
        self.assertEqual(result.returncode, 100)
        self.assertEqual(self.source.read_text(), original)
        self.assertEqual(len(self.calls()), 1)


if __name__ == '__main__':
    unittest.main()
