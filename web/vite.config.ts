import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig(({ command }) => {
  const upstream = process.env.CLOUDLET_API_UPSTREAM;
  const port = Number(process.env.CLOUDLET_WEB_PORT);
  if (
    command === 'serve' &&
    (!upstream || !Number.isInteger(port) || port < 1024 || port > 65535)
  ) {
    throw new Error(
      '请通过 make dev 启动前端，或设置 CLOUDLET_API_UPSTREAM 和 CLOUDLET_WEB_PORT',
    );
  }
  return {
    plugins: [react()],
    server: {
      host: '127.0.0.1',
      port,
      strictPort: true,
      proxy: { '/api': { target: upstream } },
    },
    build: { sourcemap: true },
  };
});
