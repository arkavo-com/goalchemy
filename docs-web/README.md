# Goalchemy documentation site

Astro + Starlight documentation for [goalchemy.dev](https://goalchemy.dev), deployed at the domain root through GitHub Pages. The site uses the navy, violet, and blue branding from `docs/goalchemy.png`. It follows Yarcl's static Starlight and pnpm setup; no server adapter or React integration is needed.

## Local development

Use Node 24 and the pnpm version pinned in the root `package.json`. Run from the repository root:

```sh
pnpm install --frozen-lockfile
pnpm docs:dev
pnpm docs:check
pnpm docs:build
pnpm docs:preview
```

Development serves on Astro's default port, 4321. Production preview includes the Pagefind search index; the development server does not build search. `docs:build` fails on broken internal URLs, fragment identifiers, or referenced local assets, and checks the domain file and sitemap. Build output is `docs-web/dist/`.

## Content ownership

- Author tutorials, task guides, target introductions, and compiler orientation under `src/content/docs/`.
- Edit the canonical language spec, usage guide, target implementation references, and other imported documents in `specs/` and `docs/`.
- Edit runtime API sources and contracts, then run `make spec-generate` to refresh `docs/library.md` before publishing it.
- Edit `specs/features.yaml` to update the checked feature matrix alongside compiler fixtures.

`scripts/sources.mjs` maps repository documents to site URLs and sidebar metadata. `scripts/prepare-docs.mjs` runs before development, checking, and building. It publishes those documents, splits the runtime API reference into individual `std/` and `lib/` package pages, and creates the feature matrix. Generated Markdown is ignored by Git. Page edit links point at the canonical source rather than the generated output.

Markdown links to published source documents are rewritten to site URLs. Runtime-reference links retain their intended package or symbol destination after splitting. Other existing repository paths link to GitHub. References to an adjacent SDK checkout outside this repository render as prose, since those files are not part of this public site. A missing in-repository reference fails preparation.

New canonical documents need a source mapping. Keep generated output paths in `.gitignore` aligned with the mapping. Do not place authored content inside the generated `runtime/` or `targets/details/` directories. Restart `docs:dev` after editing an imported source or the feature manifest to rerun preparation; authored site content updates through Astro's normal watcher.

`public/llms.txt` is served unchanged at `/llms.txt` and linked from the documentation sidebar, introduction, and page metadata. Keep it as a concise navigation guide to canonical Markdown sources. Link to maintained references instead of copying release numbers, toolchain versions, defaults, or API inventories. Repository links track `main`; agents using a release are directed to the matching tag. The docs build checks its site URLs and repository paths, so moved or removed references fail the build.

The social image is copied from the existing README graphic during preparation. The Erlenmeyer-flask SVG logo in `src/assets/goalchemy.svg` is also copied to the favicon path during preparation. The SVG hero diagram includes all eight targets; Swift remains explicitly experimental. Do not add analytics or external font dependencies without a deliberate project decision.

## Homepage code example

The two-column source/output viewer uses real compiler output from `src/examples/tasks.go.txt`. Native read-only textareas sit over synchronized, decorative syntax-highlighted layers; Shiki runs at build time, and no highlighter or editor framework is shipped to the browser. The selected output loads from a static JSON asset, with subsequent tab selections cached locally. Examples are display-only and do not call the task API from the browser.

After changing the source or compiler, regenerate the committed output snapshots:

```sh
pnpm --filter docs-web examples:generate
```

This requires the reference Go toolchain but no target runtimes. The generator builds the local compiler and uses `--pause-model=native --names=source --line-directives=false` for the seven targets that support native pausing; C uses `--pause-model=frames`. It emits the cooperative example to all eight targets in a temporary directory and retains each root package's source file under `src/examples/generated/`. Keeping `.go` samples as `.txt` avoids adding documentation fixtures to the compiler's Go package discovery. Normal docs builds use the snapshots and require only Node/pnpm. A source hash check rejects stale examples. Generation settings and each target's pause model are recorded in `src/examples/generated.json`.

The documentation sidebar uses plain section headings and Starlight's standard links and collapsible subgroups. Automatically created directory groups are flattened, so package names such as `std/encoding/json` are directly available inside their package category.

## GitHub Pages

`.github/workflows/docs.yml` installs the locked pnpm dependencies, checks Astro, builds the site, and validates links on relevant pull requests. Pushes to `main` affecting the docs, specs, site tooling, or workflow also upload `docs-web/dist` and deploy it through the `github-pages` environment. Manual runs on `main` support a deliberate redeploy. Pull requests only build.

Before the first production deployment, the repository owner must configure **Settings → Pages → Build and deployment → Source: GitHub Actions**, set the custom domain to `goalchemy.dev`, and configure the domain's DNS for GitHub Pages. Enable HTTPS when the certificate is ready. GitHub Pages settings and DNS are external configuration; the workflow does not change them.

The Astro `site` is `https://goalchemy.dev`, `public/CNAME` contains `goalchemy.dev`, and no repository subpath is configured. Keep those values and `public/robots.txt` aligned if the production domain changes. `pnpm docs:preview` can verify the artifact locally before merge.
