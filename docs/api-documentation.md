# Consumer API documentation

`goalchemy docs` documents a library's exported Go API and its compiler-generated
interfaces for Go, TypeScript, Python, Java, C#, Rust, C, and experimental Swift.
Use the JSON catalog for custom tooling, portable Markdown for an existing docs
site, or the Astro/Starlight template for a new site. No existing website is
required.

## Generate a reference

Build Goalchemy from this repository, then select the SDK's library packages:

```sh
go build -o bin/goalchemy ./cmd/goalchemy
bin/goalchemy docs -C /path/to/sdk -format json -out /tmp/sdk-api-json ./src
bin/goalchemy docs -C /path/to/sdk -format markdown -out /tmp/sdk-api-md ./src
bin/goalchemy docs -C /path/to/sdk -format site -out /tmp/sdk-api-site ./src
```

The input must pass the compiler's Go subset checks and contain non-`main`
library packages. Package patterns, module resolution, build tags, and gates
work as they do for `compile`. Documentation generation emits each selected
target into a temporary directory to capture its actual public declarations;
it does not invoke native compilers or install target toolchains.

| Flag | Meaning |
| --- | --- |
| `-format json\|markdown\|site` | Output format; defaults to `json`. |
| `-out <directory>` | Required destination directory, independent of `-C`. |
| `-title <title>` | Reference and template title; defaults to `SDK API`. |
| `-target typescript,python` | Optional comma-separated targets; defaults to all eight. |
| `-C <directory>` | Source module working directory. |
| `-tags a,b` | Go build tags. |
| `-gate sequential\|cooperative` | Compiler language gate. |
| `-config <goalchemy.yaml>` | Use configured packages, tags, gate, and targets. |
| `-json` | Emit source diagnostics as JSON on stderr. |

With `-config`, package patterns come from the configuration. Explicit `-C`,
`-tags`, `-gate`, or `-target` flags override the corresponding setting. Output
paths in the build configuration are not used by `docs`.

Source loading or subset errors fail the command. A target-specific emission
failure is recorded as `unavailable`, with its compiler diagnostic, while the
source reference and other targets are still generated. Generation exits
successfully in that case; consumers requiring every language should check
`targets[].status` in the JSON catalog.

For the OpenTDF SDK, both its current handle API and its older value facade can
be documented together:

```sh
goalchemy docs -C /path/to/tdf/sdk -gate cooperative \
  -title 'OpenTDF SDK API' -format markdown -out /tmp/opentdf-reference \
  ./src ./src/library
```

## Embed Markdown in an existing site

Markdown output contains `index.md`, `packages/`, `symbols/`, and `api.json`.
Pages use ordinary Markdown, fenced code blocks, and relative `.md` links.
There are no required Astro components, MDX imports, plugins, or frontmatter.
Keep this directory structure when embedding the reference and configure the
host site's Markdown routing as needed. A site that maps `.md` links to routes
can render the files directly; another renderer may need its normal link
rewrite step.

Each exported type and method has its own page. Package indexes link to public
declarations; type pages link to their methods; parameters, results, and fields
link to referenced named types. Go doc links such as `[Config]` or
`[Client.Encrypt]` resolve to local reference pages where possible.

Regenerate inside the same output directory to update the reference and remove
obsolete generated pages. Keep `.goalchemy-docs.json`: it tracks generated
files and their hashes. Unrelated files are preserved, and an edited reference
file causes an error before any update is written. Update Go comments instead
of generated pages. To switch formats, use a separate destination directory.

## Start a Starlight site

```sh
goalchemy docs -C /path/to/sdk -format site -title 'Example SDK' \
  -out /tmp/example-sdk-docs ./src
cd /tmp/example-sdk-docs
pnpm install
pnpm check
pnpm dev
pnpm build
```

Use Node 24 and the pnpm version pinned in the generated `package.json`. Commit
the lockfile created by the first install, then use `pnpm install
--frozen-lockfile` in CI. Production output is `dist/`, including Starlight's
Pagefind search. This is an ordinary static Astro site that can be hosted on
GitHub Pages or another static host.

Set `site` in `astro.config.mjs` to the public origin. Set `base` as well if the
site will live under a repository path. The template uses a trailing slash on
routes, and its generated reference links follow that convention. The compiler
does not publish the site or choose a domain.

The template adds a homepage, navigation, search, theme CSS, and API reference
pages at `src/content/docs/api/`. Edit the homepage and add your own guides.
Symbol pages are searchable and reachable from package/type pages without
putting every method in the sidebar. `public/api.json` exposes the catalog for
custom API browsers. Regeneration preserves existing scaffold files, including
your Astro configuration and homepage, while updating generated reference
pages. The scaffold dependencies can be maintained like any other Astro site.

## JSON contract

`api.json` is the common machine-readable model behind all three formats.
`schema_version: 1` identifies the contract. It contains:

- `compiler` and `title`: compiler version and reference title.
- `packages`: package IDs (import paths), descriptions, root status, and symbol IDs.
- `symbols`: public declarations, comments, deprecation notices, source signatures,
  locations, fields, parameters, results, methods, related types, and native bindings.
- `targets`: selected language names, availability, and failure reasons.

Symbol IDs use `import/path#Name` or `import/path#Type.Method`; pointer and value
receivers share the same method identity. IDs do not depend on private emitted
names or source line numbers. Filenames use readable stems and hashes to avoid
collisions between packages. Catalog ordering is deterministic, and output
does not contain absolute source paths or build timestamps.

`root` marks symbols from explicitly selected packages. Public declarations
from those packages are included, together with reachable named dependency
types needed to understand their signatures. Dependency type definitions do
not expand into a complete dependency API reference.

Each `bindings` entry records `target`, native `name`, and `signature`. C's
generic value interface also records `invocation` with the function's call
selector. These declarations come from library emitters, preserving each
target's actual async operations, error handling, byte containers, and options.
They are not guessed by replacing Go type names.

The reference covers the compiler-generated library boundary. Source constants,
variables, aliases, interfaces, and implementation methods can appear in the
source reference without a separate native declaration. Some native signatures
use helper types provided by the generated library/runtime. Handwritten SDK host
adapters, distribution packages, and convenience APIs need their own additional
documentation. The generator does not infer those APIs from Go comments.
