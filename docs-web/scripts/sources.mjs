// The repository documents remain the authority. These entries only define publication metadata.
export const sources = [
  ['docs/rust-no-std.md', 'targets/details/rust-no-std', 'Rust: no_std profile', 'Embedded Rust with core and alloc, memory controls, and supported scope.', 1],
  ['docs/api-documentation.md', 'guides/api-documentation', 'Generate SDK documentation', 'Consumer API catalogs, portable Markdown, and an Astro Starlight site template.', 7],
  ['docs/usage.md', 'guides/usage', 'Commands and output', 'CLI usage, project configuration, generated names, and output layouts.', 8],
  ['specs/language.md', 'language/specification', 'Language specification', 'The accepted Go subset, semantics, feature gates, and compatibility contract.', 2],
  ['docs/float-support.md', 'language/floating-point', 'Floating-point semantics', 'IEEE floating-point behavior, target representations, and differential verification.', 4],
  ['specs/diagnostics.md', 'reference/diagnostics', 'Diagnostics', 'Stable diagnostic categories and the information carried by text and JSON output.', 2],
  ['docs/library-handles.md', 'guides/library-handles', 'Stateful library handles', 'Retain source state across native library calls with explicit ownership and lifetime.', 4],
  ['docs/protobuf.md', 'guides/protobuf', 'Protobuf and Connect', 'Generate subset-Go messages, proto3 JSON codecs, and unary Connect clients.', 5],
  ['docs/host-operations.md', 'guides/host-operations', 'Host operations', 'Ownership, cancellation, clocks, and completion of native host operations.', 6],
  ['docs/checksum.md', 'reference/checksums', 'CRC32 host mappings', 'IEEE CRC32 implementations, native dependencies, and portable fallbacks.', 3],
  ['docs/adding-language.md', 'internals/adding-a-target', 'Adding a target', 'Implement a new target emitter, runtime, native capabilities, and conformance checks.', 3],
  ['docs/hardening.md', 'internals/testing', 'Testing and hardening', 'Differential testing, fuzzing, reproducible output, and toolchain upgrades.', 4],
  ['docs/ci.md', 'internals/ci', 'Compiler CI', 'Pinned prerequisites, compiler test suites, and local reproduction of CI jobs.', 5],
];

for (const [target, label] of Object.entries({ go: 'Go', typescript: 'TypeScript', python: 'Python', java: 'Java', csharp: 'C#', rust: 'Rust', c: 'C', swift: 'Swift' })) {
  const kinds = target === 'swift' ? [['target', 'setup and library API']] : [
    ['library-boundary', 'library boundary'], ['host-operations', 'host operations'], ['byte-storage', 'byte storage'],
  ];
  for (const [kind, title] of kinds) {
    // Go uses the general host-operation guide and has no separate byte-storage guide.
    if (target === 'go' && kind !== 'library-boundary') continue;
    if (['python', 'c'].includes(target) && kind === 'library-boundary') continue;
    sources.push([`docs/${target}-${kind}.md`, `targets/details/${target}-${kind}`, `${label}: ${title}`, `${label} target ${title}, implementation details, and verification evidence.`, 1]);
  }
}
