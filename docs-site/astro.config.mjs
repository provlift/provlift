import { defineConfig } from 'astro/config';
import tailwindcss from '@tailwindcss/vite';

// Published as a GitHub Pages project site at https://provlift.github.io/provlift/,
// so bundled assets must be served from the /provlift/ base path.
export default defineConfig({
  site: 'https://provlift.github.io',
  base: '/provlift/',
  vite: {
    plugins: [tailwindcss()],
  },
});
