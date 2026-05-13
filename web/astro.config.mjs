import { defineConfig } from 'astro/config';

export default defineConfig({
  site: 'https://bairn.dunn.dev',
  output: 'static',
  trailingSlash: 'always',
  build: {
    format: 'directory',
  },
});
