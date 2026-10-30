#!/usr/bin/env node
/**
 * T22 唯一验收入口：npm run test:debug。
 *
 * 依次执行：
 *   1. repo 根：go test -cover ./internal/adminapi
 *   2. web 根：vitest run src/features/debug/DebugPage.test.tsx
 *
 * 全部通过以 0 退出；任一失败（包括命令无法启动/被信号终止/缺少本地
 * Vitest 安装）以非零退出。子进程使用 Node spawnSync + shell:false 直接
 * 启动 argv，不经过 shell。
 */
import { spawnSync } from 'node:child_process';
import { existsSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import process from 'node:process';

const scriptDir = dirname(fileURLToPath(import.meta.url));
const webRoot = resolve(scriptDir, '..');
const repoRoot = resolve(webRoot, '..');

function run(label, command, args, cwd) {
  console.log(`\n[test:debug] ${label}`);
  console.log(`[test:debug] cwd=${cwd}`);

  const result = spawnSync(command, args, {
    cwd,
    env: process.env,
    stdio: 'inherit',
    shell: false,
    windowsHide: true,
  });

  if (result.error) {
    console.error(`[test:debug] 无法启动 ${command}: ${result.error.message}`);
    return 1;
  }
  if (result.signal) {
    console.error(`[test:debug] ${command} 被信号 ${result.signal} 终止`);
    return 1;
  }
  return result.status ?? 1;
}

const goStatus = run(
  'go test -cover ./internal/adminapi',
  'go',
  ['test', '-cover', './internal/adminapi'],
  repoRoot,
);

const vitestEntry = join(webRoot, 'node_modules', 'vitest', 'vitest.mjs');
let vitestStatus;
if (!existsSync(vitestEntry)) {
  console.error(
    `[test:debug] 缺少 Vitest 入口 ${vitestEntry}；请先在 web 目录执行 npm install。`,
  );
  vitestStatus = 1;
} else {
  vitestStatus = run(
    'vitest run src/features/debug/DebugPage.test.tsx',
    process.execPath,
    [vitestEntry, 'run', 'src/features/debug/DebugPage.test.tsx'],
    webRoot,
  );
}

if (goStatus !== 0 || vitestStatus !== 0) {
  console.error(`\n[test:debug] 失败：go=${goStatus} vitest=${vitestStatus}`);
  process.exitCode = 1;
} else {
  console.log('\n[test:debug] 全部通过');
}
