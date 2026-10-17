// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

export default defineConfig({
  site: 'https://goalchemy.dev',
  output: 'static',
  trailingSlash: 'always',
  integrations: [
    starlight({
      title: 'Goalchemy',
      description: 'Write your SDK once in a restricted subset of Go. Generate Go, TypeScript, Python, Java, C#, Rust, C, and Swift with consistent behavior.',
      logo: { src: './src/assets/goalchemy.svg' },
      favicon: '/favicon.svg',
      social: [{ icon: 'github', label: 'GitHub', href: 'https://github.com/eugenioenko/goalchemy' }],
      editLink: { baseUrl: 'https://github.com/eugenioenko/goalchemy/edit/main/docs-web/' },
      customCss: ['./src/styles/custom.css'],
      head: [
        { tag: 'meta', attrs: { property: 'og:image', content: 'https://goalchemy.dev/social.png' } },
        { tag: 'meta', attrs: { name: 'twitter:card', content: 'summary_large_image' } },
        { tag: 'meta', attrs: { name: 'theme-color', content: '#0e1b30' } },
      ],
      sidebar: [
        { label: 'Getting started', items: [{ autogenerate: { directory: 'getting-started' } }] },
        { label: 'Guides', items: [{ autogenerate: { directory: 'guides' } }] },
        { label: 'Language', items: [{ autogenerate: { directory: 'language' } }] },
        {
          label: 'Targets',
          items: [
            { label: 'Overview', link: '/targets/' },
            ...['go', 'typescript', 'python', 'java', 'csharp', 'rust', 'c', 'swift'].map((target) => ({
              label: { go: 'Go', typescript: 'TypeScript', python: 'Python', java: 'Java', csharp: 'C#', rust: 'Rust', c: 'C', swift: 'Swift (experimental)' }[target] ?? target,
              link: `/targets/${target}/`,
            })),
            { label: 'Implementation details', collapsed: true, items: [{ autogenerate: { directory: 'targets/details' } }] },
          ],
        },
        {
          label: 'Runtime reference',
          items: [
            { label: 'Overview', link: '/runtime/overview/' },
            { label: 'Standard packages', collapsed: true, items: [{ autogenerate: { directory: 'runtime/std' } }] },
            { label: 'Host capabilities', collapsed: true, items: [{ autogenerate: { directory: 'runtime/lib' } }] },
          ],
        },
        { label: 'Reference', items: [{ autogenerate: { directory: 'reference' } }] },
        { label: 'Compiler development', collapsed: true, items: [{ autogenerate: { directory: 'internals' } }] },
      ],
    }),
  ],
});
