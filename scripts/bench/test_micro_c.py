import os
from pathlib import Path
import sys
import tempfile
import unittest

from micro_c import measured


class Measurements(unittest.TestCase):
    def test_output_and_per_child_rss(self):
        with tempfile.TemporaryDirectory() as work:
            _, large, output = measured([sys.executable, '-c',
                "a=bytearray(64*1024*1024); print(len(a))"], Path(work), os.environ)
            _, small, _ = measured([sys.executable, '-c', "print('small')"], Path(work), os.environ)
            self.assertEqual(output, b'67108864\n')
            self.assertGreater(large, small + 32*1024*1024)

    def test_failure_is_not_a_sample(self):
        with self.assertRaises(RuntimeError):
            measured([sys.executable, '-c', 'raise SystemExit(3)'], Path.cwd(), os.environ)


if __name__ == '__main__':
    unittest.main()
