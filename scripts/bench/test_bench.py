import subprocess
import sys
import unittest

import bench


@unittest.skipUnless(sys.platform in ('linux', 'darwin'), 'wait4 RSS platform')
class MeasurementTests(unittest.TestCase):
    def test_each_child_has_its_own_peak(self):
        _, _, large = bench.measured([sys.executable, '-c',
                                     'x = bytearray(64 * 1024 * 1024); print(len(x))'])
        seconds, out, small = bench.measured([sys.executable, '-c',
                                             'import sys; print("out"); print("err", file=sys.stderr)'])
        self.assertGreater(seconds, 0)
        self.assertEqual(out, 'out\nerr\n')
        self.assertGreater(small, 0)
        self.assertGreater(large - small, 32 * 1024 * 1024)

    def test_failure_includes_output(self):
        with self.assertRaises(subprocess.CalledProcessError) as caught:
            bench.measured([sys.executable, '-c', 'print("failed"); raise SystemExit(7)'])
        self.assertEqual(caught.exception.returncode, 7)
        self.assertEqual(caught.exception.output, 'failed\n')


if __name__ == '__main__':
    unittest.main()
