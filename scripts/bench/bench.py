#!/usr/bin/env python3
"""Opt-in Goalchemy benchmarks (docs/benchmarks.md). Not run by go test or CI.

  bench.py micro [--targets go,typescript,swift] [--pause-model frames|native] [--runs 7] NAME[@MODEL]=COMPILER ...
  bench.py swift-matrix [--variants A,B,C] [--blocks 50,200,1000] [--modes debug,release] NAME[@MODEL]=COMPILER ...
  bench.py csharp-matrix [--variants C] [--blocks 50,200,1000] NAME[@MODEL]=COMPILER ...
  bench.py swift-sdk --sdk DIR [--runs 1] NAME=COMPILER ...

Each NAME=COMPILER is a goalchemy binary; several compare builds A/B. Timings
alternate compilers per run. Results print as a table and are written as
JSON to --json (default bench-<command>.json in the work directory)."""
import argparse, json, os, re, shutil, statistics, subprocess, sys, time

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
PROGRAMS = ('cpu', 'calls', 'pingpong')


def run(cmd, cwd=None, env=None):
    return subprocess.run(cmd, cwd=cwd, env=env, check=True, capture_output=True, text=True).stdout


def output(cmd, cwd=None, env=None):
    p = subprocess.run(cmd, cwd=cwd, env=env, check=True, capture_output=True, text=True)
    return p.stdout + p.stderr


def timed(cmd, cwd=None, env=None):
    start = time.perf_counter()
    out = output(cmd, cwd, env)
    return time.perf_counter() - start, out


def stats(xs):
    return {'median': statistics.median(xs), 'min': min(xs), 'max': max(xs), 'n': len(xs)}


def compilers(specs):
    out = {}
    for s in specs:
        name, _, path = s.partition('=')
        if not path:
            sys.exit('compiler must be NAME=PATH: ' + s)
        out[name] = os.path.abspath(path)
    return out


def toolchain_env():
    env = dict(os.environ)
    tc = os.path.join(ROOT, '.toolchains')
    swift = os.path.join(tc, 'swift-6.4.0', 'usr', 'bin')
    if shutil.which('swiftc') is None and os.path.isdir(swift):
        env['PATH'] = swift + os.pathsep + env['PATH']
    if subprocess.run(['pkg-config', '--exists', 'libcurl'], env=env).returncode != 0 and os.path.isdir(os.path.join(tc, 'pkgconfig')):
        env['PKG_CONFIG_PATH'] = os.path.join(tc, 'pkgconfig') + os.pathsep + env.get('PKG_CONFIG_PATH', '')
    dotnet = os.path.join(tc, 'dotnet')
    if shutil.which('dotnet') is None and os.path.isfile(os.path.join(dotnet, 'dotnet')):
        env['PATH'] = dotnet + os.pathsep + env['PATH']
        env['DOTNET_ROOT'] = dotnet
    return env


NATIVE_PAUSE = ('go', 'typescript', 'swift', 'csharp', 'python', 'rust')


def csharp_build(out, env):
    return timed(['sh', '-c', "sed '/^exec /d' run.sh | sh"], cwd=out, env=env)[0]


def micro(args, comps, work):
    env = toolchain_env()
    runners = {
        'rust': (lambda out: run(['cargo', 'build', '--release', '--quiet'], cwd=out, env=env), ['target/release/main']),
        'go': (lambda out: run(['go', 'build', '-o', 'prog', '.'], cwd=out), ['./prog']),
        'typescript': (None, ['node', 'main.ts']),
        'python': (None, ['python3', 'main.py']),
        'swift': (lambda out: swift_build(out, 'release', env, False), ['.build/prog-release']),
        'csharp': (lambda out: csharp_build(out, env), ['dotnet', 'bin/main.dll']),
    }
    res = {}
    for prog in args.programs.split(','):
        src = os.path.join(HERE, 'testdata', prog)
        for target in args.targets.split(','):
            build, cmd = runners[target]
            dirs = {}
            for name, comp in comps.items():
                out = os.path.join(work, f'{prog}-{target}-{name}')
                shutil.rmtree(out, ignore_errors=True)
                model = name.partition('@')[2] or args.pause_model
                flags = ['-pause-model', model] if target in NATIVE_PAUSE and model else []
                run([comp, 'compile', '-gate', 'cooperative', '-target', target, *flags, '-out', out, '.'], cwd=src)
                if build:
                    build(out)
                dirs[name] = out
            times = {n: [] for n in dirs}
            outputs = {n: set() for n in dirs}
            for i in range(args.runs):
                names = list(dirs) if i % 2 == 0 else list(reversed(dirs))
                for n in names:
                    t, o = timed(cmd, cwd=dirs[n], env=env)
                    times[n].append(t)
                    outputs[n].add(o)
            key = f'{prog}/{target}'
            res[key] = {n: {**stats(v), 'output': sorted(outputs[n])} for n, v in times.items()}
            print(key, ' '.join(f'{n}={statistics.median(v) * 1000:.0f}ms' for n, v in times.items()),
                  'outputs agree' if len({tuple(sorted(o)) for o in outputs.values()}) == 1 else 'OUTPUTS DIFFER', flush=True)
    return res


def segments(n):
    segs, b, i = [], 0, 0
    while b < n:
        if i % 2 == 0:
            segs.append(('if', i % 7, i % 5 + 1, i % 3 + 1)); b += 3
        else:
            segs.append(('loop', 900 + i)); b += 8
        i += 1
    return segs


def matrix_program(variant, blocks):
    """A: one function with about blocks basic blocks; B: blocks/4 small
    functions; C: A with a channel send and receive every ~10 segments."""
    if variant == 'B':
        fns, calls = [], []
        for i in range(max(1, blocks // 4)):
            fns.append(f'func g{i}(x int) int {{\n\tif x%7 == {i % 7} {{\n\t\tx += {i % 5 + 1}\n\t}} else {{\n\t\tx -= {i % 3 + 1}\n\t}}\n\tif x > {i}000 {{\n\t\treturn x - 1\n\t}}\n\treturn x\n}}\n')
            calls.append(f'\tx = g{i}(x)')
        return 'package main\n\n' + '\n'.join(fns) + '\nfunc main() {\n\tx := 5\n' + '\n'.join(calls) + '\n\tprintln(x)\n}\n'
    pause = variant == 'C'
    lines = ['\tx := n']
    for k, s in enumerate(segments(blocks)):
        if pause and k % 10 == 5:
            lines += ['\tch <- x', '\tx = <-ch']
        if s[0] == 'if':
            lines += [f'\tif x%7 == {s[1]} {{', f'\t\tx += {s[2]}', '\t} else {', f'\t\tx -= {s[3]}', '\t}']
        else:
            lines += ['\tfor j := 0; j < 3; j++ {', '\t\tif j == 1 {', '\t\t\tcontinue', '\t\t}',
                      f'\t\tif x > {s[1]}000 {{', '\t\t\tbreak', '\t\t}', '\t\tx += j', '\t}']
    lines.append('\treturn x')
    params = 'ch chan int, n int' if pause else 'n int'
    call = 'f(make(chan int, 1), 5)' if pause else 'f(5)'
    return f'package main\n\nfunc f({params}) int {{\n' + '\n'.join(lines) + f'\n}}\n\nfunc main() {{\n\tprintln({call})\n}}\n'


def write_matrix(work, variant, blocks):
    src = os.path.join(work, f'matrix-{variant}-{blocks}')
    shutil.rmtree(src, ignore_errors=True)
    os.makedirs(src)
    open(os.path.join(src, 'go.mod'), 'w').write(f'module t\n\ngo 1.25\n\nrequire github.com/eugenioenko/goalchemy v0.0.0\n\nreplace github.com/eugenioenko/goalchemy => {ROOT}\n')
    open(os.path.join(src, 'main.go'), 'w').write(matrix_program(variant, blocks))
    return src


def swift_build(gen, mode, env, execute=True):
    os.makedirs(os.path.join(gen, '.build', 'native'), exist_ok=True)
    flags = run(['pkg-config', '--cflags', 'openssl', 'zlib', 'libcurl'], env=env).split()
    libs = run(['pkg-config', '--libs', 'openssl', 'zlib', 'libcurl'], env=env).split()
    run(['cc', '-O2', '-Wno-deprecated-declarations', '-c', 'rt/native/GoalchemyNative.c', '-o', '.build/native/native.o', *flags], cwd=gen, env=env)
    files = re.findall(r"^        '(.*)'", open(os.path.join(gen, 'run.sh')).read(), re.M)
    opt = ['-Onone', '-j', '2'] if mode == 'debug' else ['-O', '-wmo']
    t, _ = timed(['swiftc', '-swift-version', '5', '-suppress-warnings', *opt, '-I', 'rt/native', *files, '.build/native/native.o', *libs, '-o', f'.build/prog-{mode}'], cwd=gen, env=env)
    return t, output([f'.build/prog-{mode}'], cwd=gen, env=env).strip() if execute else ''


def swift_matrix(args, comps, work):
    env = toolchain_env()
    res = {}
    for variant in args.variants.split(','):
        for blocks in map(int, args.blocks.split(',')):
            src = write_matrix(work, variant, blocks)
            for name, comp in comps.items():
                gen = os.path.join(src, 'swift-' + name.replace('@', '-'))
                gate = ['-gate', 'cooperative'] if variant == 'C' else []
                model = name.partition('@')[2] or args.pause_model
                flags = ['-pause-model', model] if model else []
                run([comp, 'compile', *gate, '-target', 'swift', *flags, '-out', gen, '.'], cwd=src)
                for mode in args.modes.split(','):
                    t, out = swift_build(gen, mode, env)
                    res[f'{variant}/{blocks}/{mode}/{name}'] = {'seconds': t, 'output': out}
                    print(f'{variant} {blocks:5} {mode:7} {name:10} {t:7.1f}s output={out}', flush=True)
    return res


def csharp_matrix(args, comps, work):
    env = toolchain_env()
    res = {}
    for variant in args.variants.split(','):
        for blocks in map(int, args.blocks.split(',')):
            src = write_matrix(work, variant, blocks)
            for name, comp in comps.items():
                gen = os.path.join(src, 'csharp-' + name.replace('@', '-'))
                gate = ['-gate', 'cooperative'] if variant == 'C' else []
                model = name.partition('@')[2] or args.pause_model
                flags = ['-pause-model', model] if model else []
                run([comp, 'compile', *gate, '-target', 'csharp', *flags, '-out', gen, '.'], cwd=src)
                build = csharp_build(gen, env)
                t, out = timed(['dotnet', 'bin/main.dll'], cwd=gen, env=env)
                res[f'{variant}/{blocks}/{name}'] = {'build_seconds': build, 'run_seconds': t, 'output': out.strip()}
                print(f'{variant} {blocks:5} {name:10} build {build:6.1f}s run (JIT) {t:6.2f}s output={out.strip()}', flush=True)
    return res


def swift_sdk(args, comps, work):
    env = toolchain_env()
    res = {}
    for name, comp in comps.items():
        out = os.path.join(work, 'sdk-swift-' + name)
        shutil.rmtree(out, ignore_errors=True)
        run([comp, 'compile', '-gate', 'cooperative', '-target', 'swift', '-out', out, './src/library', './src'], cwd=args.sdk)
        times = []
        for _ in range(args.runs):
            shutil.rmtree(os.path.join(out, '.build'), ignore_errors=True)
            t, _ = timed(['swift', 'build', '-c', 'debug', '-j', '2', '--target', 'GoalchemyGenerated'], cwd=out, env=env)
            times.append(t)
        size = sum(os.path.getsize(os.path.join(d, f)) for d, _, fs in os.walk(out) if '.build' not in d for f in fs if f.endswith('.swift') and not d.endswith('rt/types'))
        res[name] = {**stats(times), 'generated_swift_bytes': size}
        print(f'sdk swift debug {name:10} {statistics.median(times):7.1f}s generated={size} bytes', flush=True)
    return res


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument('command', choices=['micro', 'swift-matrix', 'csharp-matrix', 'swift-sdk'])
    ap.add_argument('compilers', nargs='+', help='NAME=PATH to a goalchemy binary')
    ap.add_argument('--work', default=os.path.join(ROOT, 'out', 'bench'))
    ap.add_argument('--json')
    ap.add_argument('--runs', type=int, default=7)
    ap.add_argument('--programs', default=','.join(PROGRAMS))
    ap.add_argument('--targets', default='go,typescript')
    ap.add_argument('--pause-model', default='')
    ap.add_argument('--variants', default='A,B,C')
    ap.add_argument('--blocks', default='50,200,1000')
    ap.add_argument('--modes', default='debug,release')
    ap.add_argument('--sdk')
    args = ap.parse_args()
    comps = compilers(args.compilers)
    args.work = os.path.abspath(args.work)
    if args.sdk:
        args.sdk = os.path.abspath(args.sdk)
    os.makedirs(args.work, exist_ok=True)
    if args.command == 'micro':
        res = micro(args, comps, args.work)
    elif args.command == 'swift-matrix':
        res = swift_matrix(args, comps, args.work)
    elif args.command == 'csharp-matrix':
        res = csharp_matrix(args, comps, args.work)
    else:
        if not args.sdk:
            sys.exit('swift-sdk needs --sdk')
        if args.runs == 7:
            args.runs = 1
        res = swift_sdk(args, comps, args.work)
    path = args.json or os.path.join(args.work, f'bench-{args.command}.json')
    json.dump(res, open(path, 'w'), indent=1)
    print('wrote', path)


if __name__ == '__main__':
    main()
