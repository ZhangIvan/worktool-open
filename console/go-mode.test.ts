import assert from 'node:assert/strict';
import test from 'node:test';
import { readFileSync } from 'node:fs';
import { consoleProxy, isGoMode } from './mode.ts';
import { canReadGoDevices, isGoDeviceRoute } from './src/go-policy.ts';
import type { Role } from './src/types.ts';

const read = (path: string) => readFileSync(new URL(path, import.meta.url), 'utf8');

test('Go proxy is opt-in, preserves browser Host, and cannot proxy device WebSockets', () => {
  assert.equal(isGoMode('go'), true);
  assert.deepEqual(consoleProxy('go'), { target: 'http://127.0.0.1:3101', changeOrigin: false, ws: false });
  for (const mode of ['development', 'production', 'test', '', 'GO']) {
    assert.equal(isGoMode(mode), false);
    assert.deepEqual(consoleProxy(mode), { target: 'http://127.0.0.1:3100', changeOrigin: false, ws: true });
  }
  const config = read('./vite.config.ts');
  assert.match(config, /host: '127\.0\.0\.1', port: 5173, strictPort: true/);
  assert.match(config, /'\/api': consoleProxy\(mode\)/);
  assert.match(config, /outDir: isGoMode\(mode\) \? 'dist-go' : 'dist'/);
  assert.match(read('./src/main.tsx'), /isGoMode\(import\.meta\.env\.MODE\) \? GoConsole : App/);
});

test('Go read-only route permits exact admin/operator, never owner inheritance', () => {
  for (const role of ['owner', 'admin', 'operator', 'approver', 'auditor'] as Role[]) {
    assert.equal(canReadGoDevices(role), role === 'admin' || role === 'operator');
  }
  assert.equal(isGoDeviceRoute('/devices'), true);
  for (const route of ['/overview', '/devices/new', '/devices?write=true', '/commands', '/exports', '/audit', '/planned']) {
    assert.equal(isGoDeviceRoute(route), false);
  }
});

test('Go UI only references migrated endpoints and explicitly names unavailable work', () => {
  const source = read('./src/GoConsole.tsx');
  const lifecycle = read('./src/go-session.ts');
  const endpoints = [...source.matchAll(/['"](\/api\/v1\/[^'"]+)['"]/g)].map(match => match[1]);
  assert.deepEqual(endpoints.sort(), ['/api/v1/devices']);
  assert.match(lifecycle, /kind: 'me' \| 'login' \| 'logout'/);
  assert.match(source, /isGoDeviceRoute\(route\) \? canReadGoDevices\(auth.user.role\) \? <GoDevices/);
  assert.match(source, /planned_unavailable/);
  assert.match(source, /未部署/);
  assert.match(source, /Go 服务已返回空设备列表/);
  assert.match(source, /result\.error \? <p className="error"/);
  assert.match(lifecycle, /state\.pending/);
  assert.match(source, /window\.addEventListener\('session-invalid'/);
  assert.match(source, /window\.addEventListener\('focus', check\)/);
  assert.match(source, /window\.setInterval\(check, GO_REVALIDATE_MS\)/);
  assert.match(source, /next\.dispose\(\)/);
  assert.match(source, /clearInterval\(timer\)/);
});
