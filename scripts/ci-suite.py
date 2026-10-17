#!/usr/bin/env python3
"""Run one CI suite while preserving the original short-suite package coverage."""
import argparse
import json
import os
from pathlib import Path
import re
import shlex
import subprocess

ROOT = Path(__file__).resolve().parents[1]
MODULE = 'github.com/eugenioenko/goalchemy'
INTEGRATION = MODULE + '/tests/integration'
INTEGRATION_SHARDS = 4
SPECIAL_PACKAGES = {
    'language': [MODULE + '/tests/language', MODULE + '/tests/corpus'],
    'contracts': [MODULE + '/tests/contracts'],
    'integration': [INTEGRATION],
    'memory': [MODULE + '/tests/memory', MODULE + '/tests/sanitize'],
}
FLOAT_FIXTURES = ('floats', 'co_floats', 'floats_panic32', 'floats_panic64',
                  'floats_panic_named32', 'floats_panic_named64')
SUITES = ('core', 'language', 'contracts', 'integration', 'examples', 'memory', 'floats', 'naming', 'runtime', 'pause',
          'pause-swift')
LANGUAGE_TARGET_SOURCES = ('tests/language/language_test.go', 'tests/corpus/corpus_test.go')
TARGET_NAMES = {'go': 'Go', 'typescript': 'TypeScript', 'python': 'Python', 'java': 'Java',
                'csharp': 'C#', 'rust': 'Rust', 'c': 'C', 'swift': 'Swift'}
WORKFLOW = ROOT / '.github/workflows/ci.yml'


def output(command):
    return subprocess.check_output(command, cwd=ROOT, text=True)


def package_groups():
    packages = output(['go', 'list', './...']).splitlines()
    special = {p for group in SPECIAL_PACKAGES.values() for p in group}
    missing = special.difference(packages)
    if missing:
        raise RuntimeError('missing CI packages: ' + ', '.join(sorted(missing)))
    # New packages join core automatically rather than falling out of CI.
    return dict(SPECIAL_PACKAGES, core=[p for p in packages if p not in special])


def integration_tests():
    listing = output(['go', 'test', '-list', '.', INTEGRATION])
    names = sorted(line for line in listing.splitlines()
                   if re.fullmatch(r'(?:Test|Fuzz|Example)\w*', line))
    if not names or len(names) != len(set(names)):
        raise RuntimeError('integration test discovery is empty or contains duplicates')
    return names


def integration_shard(index):
    if index < 0 or index >= INTEGRATION_SHARDS:
        raise ValueError('integration shard must be between 0 and 3')
    selected = integration_tests()[index::INTEGRATION_SHARDS]
    if not selected:
        raise RuntimeError('integration shard is empty')
    return selected


def language_targets():
    lists = []
    for source in LANGUAGE_TARGET_SOURCES:
        match = re.search(r'^var targets = \[\]string\{([^}]*)\}', (ROOT / source).read_text(), re.M)
        if not match:
            raise RuntimeError('missing targets list in ' + source)
        lists.append(re.findall(r'"([^"]+)"', match.group(1)))
    if any(targets != lists[0] for targets in lists) or len(lists[0]) != len(set(lists[0])):
        raise RuntimeError('language and corpus target lists differ or repeat')
    return lists[0]


def language_jobs():
    jobs = re.findall(r'\{name: ([^,]+), suite: language, target: ([\w]+)\}', WORKFLOW.read_text())
    targets = language_targets()
    if sorted(t for _, t in jobs) != sorted(targets):
        raise RuntimeError('language jobs must cover every fixture and corpus target exactly once')
    for name, target in jobs:
        if target not in TARGET_NAMES or name != 'Language and corpus (' + TARGET_NAMES[target] + ')':
            raise RuntimeError('language job has an unexpected name: ' + name)
    return [target for _, target in jobs]


def verify_plan():
    groups = package_groups()
    assigned = [p for group in groups.values() for p in group]
    if len(assigned) != len(set(assigned)):
        raise RuntimeError('short-suite packages overlap')
    names = integration_tests()
    shards = [names[i::INTEGRATION_SHARDS] for i in range(INTEGRATION_SHARDS)]
    if not all(shards) or sorted(n for shard in shards for n in shard) != names:
        raise RuntimeError('integration shards do not cover every test exactly once')
    return {'short_suite_packages': groups, 'integration_shards': shards, 'language_targets': language_jobs()}


def plan(suite, shard, target=None):
    environment = {}
    if suite == 'core':
        commands = [['go', 'vet', './...'],
                    ['go', 'run', './cmd/goalchemy', 'spec', 'generate', '-check'],
                    ['go', 'test', '-short', '-v', '-timeout', '30m', *package_groups()['core']]]
    elif suite == 'language':
        if target not in language_targets():
            raise ValueError('language suite needs --target, one of: ' + ', '.join(language_targets()))
        environment['GOALCHEMY_TEST_TARGETS'] = target
        parallel = ['-parallel', '2'] if target == 'swift' else []
        commands = [['go', 'test', '-v', *parallel, '-timeout', '40m', './tests/language',
                     '-run', '^TestFixtures$', '-count=1'],
                    ['go', 'test', '-v', *parallel, '-timeout', '40m', './tests/corpus',
                     '-run', '^TestRegressions$', '-count=1']]
        if target == language_targets()[0]:
            commands.append(['go', 'test', '-v', '-timeout', '15m', './tests/language',
                             '-run', '^TestFeatureManifest$', '-count=1'])
    elif suite == 'contracts':
        if shard not in (0, 1):
            raise ValueError('contracts shard must be 0 or 1')
        selection = ['-skip', '^TestHTTP'] if shard == 0 else ['-run', '^TestHTTP']
        commands = [['go', 'test', '-short', '-v', '-timeout', '30m', *selection, *SPECIAL_PACKAGES[suite]]]
    elif suite == 'integration':
        selected = integration_shard(shard)
        pattern = '^(?:' + '|'.join(re.escape(name) for name in selected) + ')$'
        commands = [['go', 'test', '-short', '-v', '-timeout', '30m', '-run', pattern, INTEGRATION]]
    elif suite == 'examples':
        commands = [['go', 'test', '-v', '-timeout', '40m', INTEGRATION,
                     '-run', '^(?:TestExamples|TestReproducibleOutput)$', '-count=1']]
    elif suite == 'memory':
        commands = [['go', 'test', '-v', '-timeout', '30m', *SPECIAL_PACKAGES[suite], '-count=1']]
    elif suite == 'floats':
        for fixture in FLOAT_FIXTURES:
            if not (ROOT / 'tests/language/testdata' / fixture / 'main.go').is_file():
                raise RuntimeError('missing float fixture: ' + fixture)
        environment['FIXTURE'] = 'floats'
        commands = [['go', 'test', '-v', '-timeout', '15m', './tests/language',
                     '-run', '^TestFixtures$', '-count=1']]
    elif suite == 'naming':
        commands = [['go', 'test', '-v', '-timeout', '15m', './tests/language',
                     '-run', '^TestNamingModes$', '-count=1']]
    elif suite == 'pause' and target == 'python':
        # The Python target under the native pause model, a job of its own to
        # keep both pause jobs within the time limit: fixtures, corpus, the
        # frames/native agreement check and the Python library/host
        # integration.
        environment['GOALCHEMY_TEST_TARGETS'] = 'python'
        environment['GOALCHEMY_PAUSE_MODEL'] = 'native'
        commands = [['go', 'test', '-v', '-timeout', '40m', './tests/language',
                     '-run', '^(?:TestFixtures|TestPythonPauseModelsAgree)$', '-count=1'],
                    ['go', 'test', '-v', '-timeout', '40m', './tests/corpus',
                     '-run', '^TestRegressions$', '-count=1'],
                    ['go', 'test', '-short', '-v', '-timeout', '30m', INTEGRATION,
                     '-run', '^TestGeneratedPython', '-count=1']]
    elif suite == 'pause':
        # The Go, TypeScript and C# targets under the native pause model:
        # fixtures, corpus, the frames/native agreement checks and the Go and
        # C# library/host integration.
        environment['GOALCHEMY_TEST_TARGETS'] = 'go,typescript,csharp'
        environment['GOALCHEMY_PAUSE_MODEL'] = 'native'
        commands = [['go', 'test', '-v', '-timeout', '40m', './tests/language',
                     '-run', '^(?:TestFixtures|TestGoPauseModelsAgree|TestTypeScriptPauseModelsAgree|TestCSharpPauseModelsAgree)$',
                     '-count=1'],
                    ['go', 'test', '-v', '-timeout', '40m', './tests/corpus',
                     '-run', '^TestRegressions$', '-count=1'],
                    ['go', 'test', '-short', '-v', '-timeout', '30m', INTEGRATION,
                     '-run', '^(?:TestGeneratedGo|TestGeneratedCSharp|TestCSharp)', '-count=1']]
    elif suite == 'pause-swift':
        # The Swift target under the native pause model (an optional job):
        # fixtures, the frames/native agreement check, the cooperative
        # fixtures with a collection at every safepoint, the corpus and the
        # Swift library integration.
        environment['GOALCHEMY_TEST_TARGETS'] = 'swift'
        environment['GOALCHEMY_PAUSE_MODEL'] = 'native'
        commands = [['go', 'test', '-v', '-parallel', '2', '-timeout', '60m', './tests/language',
                     '-run', '^(?:TestFixtures|TestSwiftPauseModelsAgree)$', '-count=1'],
                    ['env', 'GOALCHEMY_GC_THRESHOLD=1', 'FIXTURE=co_', 'go', 'test', '-v', '-parallel', '2',
                     '-timeout', '40m', './tests/language', '-run', '^TestFixtures$', '-count=1'],
                    ['go', 'test', '-v', '-parallel', '2', '-timeout', '60m', './tests/corpus',
                     '-run', '^TestRegressions$', '-count=1'],
                    ['go', 'test', '-short', '-v', '-timeout', '40m', INTEGRATION,
                     '-run', '^(?:TestGeneratedSwift|TestSwift)', '-count=1']]
    else:
        # No -short or target filter: every canonical case runs on every harness.
        commands = [['go', 'test', '-v', '-timeout', '15m', './tests/contracts',
                     '-run', '^TestTargetConformance$', '-count=1']]
    return {'suite': suite, 'shard': shard, 'target': target, 'environment': environment, 'commands': commands}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('suite', nargs='?', choices=SUITES)
    parser.add_argument('--shard', type=int, default=0)
    parser.add_argument('--target', default=None, help='language or pause suite target')
    parser.add_argument('--plan', action='store_true', help='print commands without running checks')
    parser.add_argument('--verify-plan', action='store_true', help='audit package and test coverage')
    args = parser.parse_args()
    if args.verify_plan:
        print(json.dumps(verify_plan(), indent=2))
        return
    if args.suite is None:
        parser.error('provide a suite or --verify-plan')
    if args.suite == 'core' and not args.plan:
        print(json.dumps(verify_plan(), indent=2), flush=True)
    selected = plan(args.suite, args.shard, args.target or None)
    print(json.dumps(selected, indent=2), flush=True)
    if args.plan:
        return
    environment = dict(os.environ, **selected['environment'])
    for command in selected['commands']:
        print('RUN ' + shlex.join(command), flush=True)
        subprocess.run(command, cwd=ROOT, env=environment, check=True)


if __name__ == '__main__':
    main()
