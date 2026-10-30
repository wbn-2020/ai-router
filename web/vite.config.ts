import react from '@vitejs/plugin-react';
import { defineConfig } from 'vitest/config';

// 构建产物必须落在 internal/webassets/dist：Go 的 //go:embed 只能引用
// 包目录内的文件，internal/webassets 是唯一嵌入点。
export default defineConfig({
  plugins: [react()],
  base: '/',
  build: {
    outDir: '../internal/webassets/dist',
    emptyOutDir: true,
    sourcemap: false,
  },
  server: {
    // 本机开发时把管理 API 转发给 gateway serve（默认 127.0.0.1:8080）。
    proxy: {
      '/admin/api': 'http://127.0.0.1:8080',
    },
  },
  test: {
    environment: 'jsdom',
    environmentOptions: {
      jsdom: {
        // TanStack Router 的 basepath 是 /admin；测试从管理端根路径开始。
        url: 'http://localhost/admin/',
      },
    },
    css: false,
    restoreMocks: true,
    clearMocks: true,
  },
});
