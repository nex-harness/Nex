import { execSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { defineConfig } from 'vite';
import tailwindcss from '@tailwindcss/vite';

const outDir = path.resolve(__dirname, '../../pkg/webui/dist');

function readBuildMetadata(mode: string) {
  let pkgVersion = 'dev';
  try {
    const pkg = JSON.parse(fs.readFileSync(path.resolve(__dirname, 'package.json'), 'utf8')) as {
      version?: string;
    };
    if (pkg.version) pkgVersion = pkg.version;
  } catch {
    // ignore
  }
  let gitSha = '';
  try {
    gitSha = execSync('git rev-parse --short HEAD', {
      cwd: path.resolve(__dirname, '../..'),
      stdio: ['ignore', 'pipe', 'ignore'],
    })
      .toString()
      .trim();
  } catch {
    // ignore
  }
  const appVersion = process.env.NEX_APP_VERSION || process.env.VERSION || pkgVersion || 'dev';
  const buildSHA = process.env.NEX_BUILD_SHA || process.env.COMMIT_SHA || gitSha;
  const isDev =
    mode !== 'production' ||
    appVersion === 'dev' ||
    appVersion.includes('-dev') ||
    appVersion.includes('-dirty');
  return { appVersion, buildSHA, isDev };
}

export default defineConfig(({ mode }) => {
  const meta = readBuildMetadata(mode);
  return {
    define: {
      __NEX_APP_VERSION__: JSON.stringify(meta.appVersion),
      __NEX_BUILD_SHA__: JSON.stringify(meta.buildSHA),
      __NEX_IS_DEV__: JSON.stringify(meta.isDev),
    },
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
  };
});
