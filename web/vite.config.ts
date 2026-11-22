import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';

const backendTarget = process.env.COLABHIVE_DEV_API || 'http://127.0.0.1:7860';
const wsTarget = backendTarget.replace(/^http/, 'ws');

export default defineConfig(({ mode }) => {
  const isPages = mode === 'pages' || process.env.VITE_DEMO_SITE === 'true';

  return {
    base: isPages ? process.env.VITE_BASE_PATH || './' : '/',
    define: isPages
      ? {
          'import.meta.env.VITE_DEMO_SITE': JSON.stringify('true'),
        }
      : undefined,
    plugins: [react(), tailwindcss()],
    resolve: {
      alias: {
        '@': '/src',
      },
    },
    server: {
      port: 5173,
      strictPort: false,
      proxy: {
        '/api': {
          target: backendTarget,
          changeOrigin: true,
        },
        '/mcp': {
          target: backendTarget,
          changeOrigin: true,
        },
        '/ws': {
          target: wsTarget,
          ws: true,
          changeOrigin: true,
        },
      },
    },
    build: {
      outDir: isPages ? 'dist-pages' : 'dist',
      emptyOutDir: true,
      sourcemap: false,
      target: 'es2022',
    },
  };
});
