#!/usr/bin/env python3
"""Reproducible host time + memory measurements for bounded Micro C workloads.

  python3 scripts/bench/micro_c.py --compiler /tmp/goalchemy --json /tmp/report.json

Uses a fresh process per sample and wait4 for per-child peak RSS. Arena usage
comes from the runtime; host RSS and host timings are NOT ESP32 measurements.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import statistics
import subprocess
import sys
import tempfile
import time

ROOT = Path(__file__).resolve().parents[2]

def measured(cmd, cwd, env):
    with tempfile.TemporaryFile() as stdout, tempfile.TemporaryFile() as stderr:
        started = time.perf_counter()
        p = subprocess.Popen(cmd, cwd=cwd, env=env, stdout=stdout, stderr=stderr)
        try:
            _, status, usage = os.wait4(p.pid, 0)
            p.returncode = os.waitstatus_to_exitcode(status)
        except BaseException:
            p.kill(); p.wait()
            raise
        elapsed = time.perf_counter() - started
        stdout.seek(0); stderr.seek(0)
        output = stdout.read() + stderr.read()
        if p.returncode: raise RuntimeError(f"{cmd}: {output.decode()}")
        rss = usage.ru_maxrss * (1 if sys.platform == 'darwin' else 1024)
        return elapsed, rss, output

def stats(values):
    return dict(median=statistics.median(values), min=min(values), max=max(values), n=len(values))

def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument('--compiler', required=True, type=Path)
    ap.add_argument('--json', required=True, type=Path)
    ap.add_argument('--work', type=Path)
    ap.add_argument('--runs', type=int, default=7)
    args = ap.parse_args()
    if args.runs < 1: ap.error('--runs must be positive')
    env = dict(os.environ, GOMAXPROCS='2', GOMEMLIMIT='1GiB', CFLAGS='-Os')
    compiler = args.compiler.resolve()
    def git(*args): return subprocess.check_output(['git', *args], cwd=ROOT, text=True).strip()
    report = dict(revision=git('rev-parse', 'HEAD'), dirty=bool(git('status', '--porcelain')),
                  platform=platform.platform(), machine=platform.machine(),
                  compiler_sha256=hashlib.sha256(compiler.read_bytes()).hexdigest(),
                  cc=subprocess.check_output([env.get('CC', 'cc'), '--version'], text=True).splitlines()[0],
                  cflags=env['CFLAGS'], cppflags=env.get('CPPFLAGS', ''), source_integer_bits=64,
                  measurements='Host monotonic execution time includes output callback; arena peak includes alignment. RSS is fresh child peak, not ESP32 RAM. Native build is a single compiler driver.',
                  workloads={})
    with tempfile.TemporaryDirectory(prefix='goalchemy-micro-') as temporary:
        work = args.work.resolve() if args.work else Path(temporary)
        work.mkdir(parents=True, exist_ok=True)
        print('workload    execution ms    arena peak / capacity B    host RSS MiB')
        for name in ('cpu', 'bytes', 'maps'):
            src = ROOT / 'scripts/bench/testdata' / ('micro_' + name)
            out = work / name
            measured([str(compiler), 'compile', '-target', 'c-micro', '-out', str(out), '.'], src, env)
            build_s, build_rss, _ = measured(['sh', 'build.sh'], out, env)
            reference = subprocess.run(['go', 'run', '.'], cwd=src, env=env, check=True, capture_output=True)
            expected = reference.stdout + reference.stderr
            runs = []; times = []; rss = []
            for _ in range(args.runs):
                metrics = out / 'metrics.json'
                process_s, peak_rss, output = measured(['./main'], out, dict(env, GOALCHEMY_MICRO_METRICS=str(metrics)))
                if output != expected: raise RuntimeError(f'{name} differs from Go: {output!r} != {expected!r}')
                m = json.loads(metrics.read_text()); m['process_seconds'] = process_s
                runs.append(m); times.append(m['execution_seconds']); rss.append(peak_rss)
            peak = max(m['arena_peak_bytes'] for m in runs)
            result = dict(build_seconds=build_s, build_peak_rss_bytes=build_rss,
                          executable_bytes=(out/'main').stat().st_size, execution_seconds=stats(times),
                          host_peak_rss_bytes=stats(rss), arena_peak_bytes=peak,
                          arena_capacity_bytes=runs[0]['arena_capacity_bytes'], samples=runs)
            report['workloads'][name] = result
            print(f"{name:10} {statistics.median(times)*1000:12.3f} {peak:12} / {result['arena_capacity_bytes']:<10} {statistics.median(rss)/1048576:12.2f}")
    args.json.write_text(json.dumps(report, indent=2)+'\n')

if __name__ == '__main__': main()
