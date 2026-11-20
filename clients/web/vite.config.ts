import fs from 'node:fs';
import path from 'node:path';
import { defineConfig } from 'vite';
import tailwindcss from '@tailwindcss/vite';

const outDir = path.resolve(__dirname, '../../pkg/webui/dist');

export default defineConfig({
  plugins: [
    tailwindcss(),
    {
      name: 'preserve-embed-gitkeep',
      closeBundle() {
        fs.mkdirSync(outDir, { recursive: true });
        fs.writeFileSync(path.join(outDir, '.gitkeep'), '');
      },
    },
  ],
  esbuild: {
    jsx: 'automatic',
  },
  build: {
    outDir: '../../pkg/webui/dist',
    emptyOutDir: true,
    chunkSizeWarningLimit: 1500,
  },
  test: {
    environment: 'jsdom',
    globals: true,
  },
});
