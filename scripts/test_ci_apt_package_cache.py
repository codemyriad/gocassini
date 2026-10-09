"""Check archive configuration without changing host APT configuration."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


SCRIPT = Path(__file__).with_name('ci-apt-package-cache.sh')


class PackageCacheTests(unittest.TestCase):
    def test_preserves_downloads_and_configures_only_archives(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            archives = root / 'cached packages'
            archives.mkdir()
            deb = archives / 'existing.deb'
            deb.write_bytes(b'cached package')
            config = root / 'config'
            env = dict(os.environ, APT_CONFIG_DIR=str(config))
            for _ in range(2):
                subprocess.run(['bash', str(SCRIPT), str(archives)],
                               env=env, check=True, capture_output=True)
            self.assertEqual(deb.read_bytes(), b'cached package')
            self.assertEqual((config / '99-cassini-package-cache').read_text(),
                             f'Dir::Cache::archives "{archives}";\n'
                             'APT::Keep-Downloaded-Packages "true";\n'
                             'Binary::apt::APT::Keep-Downloaded-Packages "true";\n')

    def test_rejects_invalid_config_paths(self):
        with tempfile.TemporaryDirectory() as root:
            config = Path(root) / 'config'
            for path in ['relative', '/tmp/a"b', '/tmp/a;b', '/tmp/a\\b', '/tmp/a\nb']:
                with self.subTest(path=path):
                    result = subprocess.run(['bash', str(SCRIPT), path],
                                            env=dict(os.environ, APT_CONFIG_DIR=str(config)),
                                            capture_output=True)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertFalse(config.exists())


if __name__ == '__main__':
    unittest.main()
