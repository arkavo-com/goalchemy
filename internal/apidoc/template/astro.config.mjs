import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

export default defineConfig({
  // Set site and, for project-subpath hosting, base before publishing.
  trailingSlash: 'always',
  integrations: [starlight({
    title: __TITLE__,
    customCss: ['./src/styles/custom.css'],
    sidebar: [
      { label: 'Guides', items: [{ label: 'Introduction', link: '/' }] },
      { label: 'API reference', items: [{ autogenerate: { directory: 'api' } }] },
    ],
  })],
});
