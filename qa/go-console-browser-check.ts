/** Hosted-CI-only, synthetic browser regression. Never prints fixture values, raw driver errors, URLs, or DOM. */
import { constants, lstatSync, fstatSync, readSync, realpathSync, openSync, closeSync, writeFileSync, mkdirSync, unlinkSync } from 'node:fs';
import { dirname, isAbsolute, join, resolve, basename } from 'node:path';
import assert from 'node:assert/strict';

type Phase = 'initial' | 'revoked' | 'gate-prepare' | 'gate-closed' | 'restart-prepare' | 'restarted' | 'cleanup';
type Account = { tenantId: string; loginName: string; password: string };
type Fixture = { driverOrigin: string; appOrigin: string; chromeBinary: string; profilePath: string; accounts: Record<'admin' | 'operator' | 'owner' | 'other', Account>; expected: { tenantADeviceIds: string[]; tenantBDeviceIds: string[] } };
const phases: Phase[] = ['initial', 'revoked', 'gate-prepare', 'gate-closed', 'restart-prepare', 'restarted', 'cleanup'];
const checkpoints = ['arguments', 'selftest', 'hosted-guard', 'fixture-folder', 'fixture-read', 'fixture-schema', 'profile-path', 'chrome-binary', 'profile-create', 'state-create', 'session-create', 'session-save', 'navigate', 'signin-assert', 'login-tenant', 'login-name', 'login-password', 'login-submit', 'bad-login-assert', 'identity-assert', 'devices-assert', 'tenant-empty-assert', 'owner-denied-assert', 'logout-click', 'logout-assert', 'logout-refresh', 'logout-persisted-assert', 'state-read', 'existing-url', 'reload', 'revoked-assert', 'session-delete', 'state-delete'] as const;
const categories = ['validation', 'filesystem', 'transport', 'timeout', 'http', 'webdriver', 'malformed-response', 'assertion', 'unexpected-url', 'unknown'] as const;
const webdriverErrors = ['element click intercepted', 'element not interactable', 'insecure certificate', 'invalid argument', 'invalid cookie domain', 'invalid element state', 'invalid selector', 'invalid session id', 'javascript error', 'move target out of bounds', 'no such alert', 'no such cookie', 'no such element', 'no such frame', 'no such shadow root', 'no such window', 'script timeout', 'session not created', 'stale element reference', 'timeout', 'unable to capture screen', 'unable to set cookie', 'unexpected alert open', 'unknown command', 'unknown error', 'unknown method', 'unsupported operation', 'detached shadow root'] as const;
type Diagnostic = { checkpoint: typeof checkpoints[number]; category: typeof categories[number]; actor: 'none' | 'admin-bad' | 'admin' | 'operator' | 'owner' | 'other'; httpStatus?: number; webdriverError?: typeof webdriverErrors[number] };
let diagnostic: Diagnostic = { checkpoint: 'arguments', category: 'validation', actor: 'none' };
function mark(checkpoint: Diagnostic['checkpoint'], category: Diagnostic['category'] = 'validation') { diagnostic = { checkpoint, category, actor: diagnostic.actor }; }
function categorized(category: Diagnostic['category']): never { diagnostic.category = category; throw new Error('CHECK_FAILED'); }
function decodeDriverResponse(status: number, raw: string): any {
  check(Number.isInteger(status) && status >= 100 && status <= 599);
  diagnostic.httpStatus = status;
  if (raw.length > 128_000) categorized('malformed-response');
  let parsed: any;
  try { parsed = JSON.parse(raw); } catch { categorized('malformed-response'); }
  if (!parsed || typeof parsed !== 'object' || !Object.hasOwn(parsed, 'value')) categorized('malformed-response');
  if (parsed.value && typeof parsed.value === 'object' && Object.hasOwn(parsed.value, 'error')) {
    if (webdriverErrors.includes(parsed.value.error)) diagnostic.webdriverError = parsed.value.error;
    categorized('webdriver');
  }
  if (status < 200 || status >= 300) categorized('http');
  delete diagnostic.httpStatus;
  return parsed.value;
}
function failureResult(phase: string, error?: unknown) {
  if (error && typeof error === 'object' && 'code' in error && ['ENOENT', 'EACCES', 'EPERM', 'EEXIST', 'ELOOP', 'ENOTDIR', 'EISDIR'].includes(String(error.code))) diagnostic.category = 'filesystem';
  // Construct only from fixed enum values; never spread exceptions or driver responses.
  return { phase, result: 'FAIL', diagnostic: { ...diagnostic } };
}
const fail = () => { throw new Error('CHECK_FAILED'); };
function check(value: unknown): asserts value { if (!value) fail(); }
function text(value: unknown, max = 256): value is string { return typeof value === 'string' && value.length > 0 && value.length <= max && !/[\u0000-\u001f\u007f]/.test(value); }
function safeId(value: unknown): asserts value is string { check(typeof value === 'string' && /^[A-Za-z0-9._-]{1,256}$/.test(value)); }
function navigation(url: unknown, origin: string) { check(typeof url === 'string' && (url === `${origin}/` || url === `${origin}/#/devices`)); }
function absolute(path: unknown): asserts path is string { check(typeof path === 'string' && isAbsolute(path) && resolve(path) === path && !path.includes('\0')); }
function noLinks(path: string) {
  absolute(path);
  let current = path;
  while (true) { check(!lstatSync(current).isSymbolicLink()); const parent = dirname(current); if (current === parent) break; current = parent; }
  check(realpathSync(path) === path);
}
function privatePath(path: string, kind: 'file' | 'directory') {
  noLinks(path); const stat = lstatSync(path);
  check(kind === 'file' ? stat.isFile() && stat.nlink === 1 : stat.isDirectory());
  check((stat.mode & 0o777) === (kind === 'file' ? 0o600 : 0o700));
  check(stat.uid === process.getuid?.());
}
function privateRead(path: string) {
  privatePath(path, 'file'); const before = lstatSync(path); check(before.size <= 32_768);
  const fd = openSync(path, constants.O_RDONLY | constants.O_NOFOLLOW);
  try {
    const opened = fstatSync(fd);
    check(opened.isFile() && opened.nlink === 1 && opened.uid === process.getuid?.() && (opened.mode & 0o777) === 0o600);
    check(opened.dev === before.dev && opened.ino === before.ino && opened.size === before.size && opened.mtimeMs === before.mtimeMs && opened.ctimeMs === before.ctimeMs);
    const bytes = Buffer.alloc(32_769); let used = 0;
    while (used < bytes.length) { const count = readSync(fd, bytes, used, bytes.length - used, null); if (count === 0) break; used += count; }
    check(used <= 32_768 && used === opened.size);
    const after = fstatSync(fd); const named = lstatSync(path);
    check(after.dev === opened.dev && after.ino === opened.ino && after.size === opened.size && after.mtimeMs === opened.mtimeMs && after.ctimeMs === opened.ctimeMs);
    check(!named.isSymbolicLink() && named.dev === opened.dev && named.ino === opened.ino);
    return JSON.parse(bytes.subarray(0, used).toString('utf8')) as unknown;
  } finally { closeSync(fd); }
}
function validateFixture(value: unknown): asserts value is Fixture {
  check(value && typeof value === 'object'); const f = value as Fixture;
  check(f.driverOrigin === 'http://127.0.0.1:9515' && f.appOrigin === 'http://127.0.0.1:5173');
  absolute(f.chromeBinary); absolute(f.profilePath);
  check(f.accounts && f.expected);
  for (const key of ['admin', 'operator', 'owner', 'other'] as const) {
    const a = f.accounts[key]; check(a && text(a.tenantId) && text(a.loginName) && text(a.password, 1024));
  }
  check(f.accounts.admin.tenantId === f.accounts.operator.tenantId && f.accounts.admin.tenantId === f.accounts.owner.tenantId);
  check(f.accounts.other.tenantId !== f.accounts.admin.tenantId);
  check(Array.isArray(f.expected.tenantADeviceIds) && f.expected.tenantADeviceIds.length > 0 && f.expected.tenantADeviceIds.length <= 100);
  check(f.expected.tenantADeviceIds.every(id => text(id)) && new Set(f.expected.tenantADeviceIds).size === f.expected.tenantADeviceIds.length);
  check(Array.isArray(f.expected.tenantBDeviceIds) && f.expected.tenantBDeviceIds.length === 0);
}
function hosted(env: NodeJS.ProcessEnv) {
  check(env.GITHUB_ACTIONS === 'true' && env.RUNNER_ENVIRONMENT === 'github-hosted' && env.RUNNER_OS === 'Linux');
  check(env.GITHUB_REPOSITORY === 'ZhangIvan/worktool-open' && env.GITHUB_REF === 'refs/heads/worktool/synthetic-prototype' && env.GITHUB_EVENT_NAME === 'push');
  check(env.WORKTOOL_CONSOLE_CI_ALLOW === 'isolated-synthetic-browser-only');
  // Never let environment proxy routing intercept synthetic credentials or driver traffic.
  for (const key of ['HTTP_PROXY', 'HTTPS_PROXY', 'ALL_PROXY', 'http_proxy', 'https_proxy', 'all_proxy']) check(!env[key]);
  check(!env.NODE_USE_ENV_PROXY || env.NODE_USE_ENV_PROXY === '0');
}
function selftest() {
  const a = { tenantId: 'synthetic-a', loginName: 'synthetic', password: 'synthetic-only' };
  const f = { driverOrigin: 'http://127.0.0.1:9515', appOrigin: 'http://127.0.0.1:5173', chromeBinary: '/usr/bin/chrome', profilePath: '/tmp/run/profile', accounts: { admin: a, operator: a, owner: a, other: { ...a, tenantId: 'synthetic-b' } }, expected: { tenantADeviceIds: ['synthetic-device'], tenantBDeviceIds: [] } };
  validateFixture(f); navigation(f.appOrigin + '/#/devices', f.appOrigin); safeId('session-123');
  for (const value of ['http://evil.test/', f.appOrigin + '/api/v1/devices', f.appOrigin + '/?x=1', 'http://127.0.0.1:5173.evil.test/']) assert.throws(() => navigation(value, f.appOrigin));
  for (const id of ['../session', 'x/y', '', '?x', 'x'.repeat(257)]) assert.throws(() => safeId(id));
  assert.throws(() => validateFixture({ ...f, driverOrigin: 'http://localhost:9515' }));
  assert.throws(() => validateFixture({ ...f, expected: { ...f.expected, tenantADeviceIds: [] } }));
  assert.throws(() => validateFixture({ ...f, accounts: { ...f.accounts, other: a } }));
  assert.throws(() => absolute('/tmp/../etc/file')); assert.throws(() => absolute('relative'));
  const ci = { GITHUB_ACTIONS: 'true', RUNNER_ENVIRONMENT: 'github-hosted', RUNNER_OS: 'Linux', GITHUB_REPOSITORY: 'ZhangIvan/worktool-open', GITHUB_REF: 'refs/heads/worktool/synthetic-prototype', GITHUB_EVENT_NAME: 'push', WORKTOOL_CONSOLE_CI_ALLOW: 'isolated-synthetic-browser-only' };
  hosted(ci);
  for (const patch of [{ GITHUB_REPOSITORY: 'other/repo' }, { GITHUB_REF: 'refs/heads/main' }, { GITHUB_EVENT_NAME: 'pull_request' }, { WORKTOOL_CONSOLE_CI_ALLOW: '' }, { HTTPS_PROXY: 'http://proxy.invalid' }, { NODE_USE_ENV_PROXY: '1' }]) assert.throws(() => hosted({ ...ci, ...patch }));
  assert.throws(() => hosted({})); assert.throws(() => hosted({ GITHUB_ACTIONS: 'true', RUNNER_ENVIRONMENT: 'self-hosted', RUNNER_OS: 'Linux' }));
  assert.deepEqual(decodeDriverResponse(200, '{"value":{"sessionId":"synthetic"}}'), { sessionId: 'synthetic' });
  assert.equal(decodeDriverResponse(200, '{"value":null}'), null);
  for (const [status, body, category, driverError] of [
    [500, '{"value":{"error":"session not created","message":"SECRET","stacktrace":"SECRET"}}', 'webdriver', 'session not created'],
    [404, '{"value":{"error":"untrusted SECRET","message":"SECRET"}}', 'webdriver', undefined],
    [503, '{"value":null}', 'http', undefined],
    [200, 'not JSON SECRET', 'malformed-response', undefined],
    [200, '{}', 'malformed-response', undefined],
    [200, 'x'.repeat(128001), 'malformed-response', undefined],
  ] as const) {
    mark('selftest'); assert.throws(() => decodeDriverResponse(status, body));
    assert.equal(diagnostic.category, category); assert.equal(diagnostic.httpStatus, status); assert.equal(diagnostic.webdriverError, driverError);
    assert.equal(JSON.stringify(failureResult('selftest')).includes('SECRET'), false);
  }
  mark('selftest');
}

async function run(phase: Phase, fixturePath: string) {
  mark('hosted-guard'); hosted(process.env); // Must precede any network or browser action.
  mark('fixture-folder'); absolute(fixturePath); const folder = dirname(fixturePath); privatePath(folder, 'directory');
  mark('fixture-read', 'filesystem'); const rawFixture = privateRead(fixturePath); mark('fixture-schema'); validateFixture(rawFixture); const f: Fixture = rawFixture;
  mark('profile-path'); check(dirname(f.profilePath) === folder && !['browser-session.json', basename(fixturePath)].includes(basename(f.profilePath)));
  const statePath = join(folder, 'browser-session.json'); check(fixturePath !== statePath);
  if (phase === 'cleanup') { try { lstatSync(statePath); } catch (error) { if ((error as NodeJS.ErrnoException).code === 'ENOENT') return; throw error; } }
  mark('chrome-binary'); noLinks(f.chromeBinary); const binary = lstatSync(f.chromeBinary); check(binary.isFile() && (binary.mode & 0o111) !== 0 && (binary.mode & 0o022) === 0);
  const deadline = Date.now() + (phase === 'initial' ? 44_000 : 19_000);
  let sessionId = ''; let attempts = 0;
  async function request(method: 'POST' | 'GET' | 'DELETE', path: string, body?: unknown): Promise<any> {
    if (Date.now() >= deadline) categorized('timeout');
    check(path === '/session' || /^\/session\/[A-Za-z0-9._-]{1,256}(?:\/(?:url|refresh|execute\/sync|element|element\/[A-Za-z0-9._-]{1,256}\/(?:value|click|clear)))?$/.test(path));
    let response: Response; let raw: string;
    try {
      response = await fetch(f.driverOrigin + path, { method, redirect: 'error', signal: AbortSignal.timeout(Math.min(5000, Math.max(1, deadline - Date.now()))), headers: { 'Content-Type': 'application/json' }, ...(body === undefined ? {} : { body: JSON.stringify(body) }) });
      raw = await response.text();
    } catch (error) {
      categorized(error instanceof Error && ['AbortError', 'TimeoutError'].includes(error.name) ? 'timeout' : 'transport');
    }
    return decodeDriverResponse(response.status, raw);
  }
  const path = (suffix = '') => { safeId(sessionId); return `/session/${sessionId}${suffix}`; };
  async function urlCheck() { const url = await request('GET', path('/url')); try { navigation(url, f.appOrigin); } catch { categorized('unexpected-url'); } }
  async function action(suffix: string, body: unknown) { await urlCheck(); return request('POST', path(suffix), body); }
  async function snapshot() {
    return await action('/execute/sync', { script: `const visible=e=>!!e&&e.getClientRects().length>0&&getComputedStyle(e).visibility!=='hidden'; const all=s=>Array.from(document.querySelectorAll(s)).filter(visible); return {signin:all('form.login-panel').length===1,identity:all('.identity strong').map(e=>e.innerText),rows:all('tbody tr td:first-child code').map(e=>e.innerText),body:document.body.innerText,errors:all('[role="alert"]').length,empty:all('main .empty').map(e=>e.innerText)};`, args: [] }) as { signin: boolean; identity: string[]; rows: string[]; body: string; errors: number; empty: string[] };
  }
  async function until(predicate: (s: Awaited<ReturnType<typeof snapshot>>) => boolean) {
    for (let i = 0; i < 40 && Date.now() < deadline; i++) { if (predicate(await snapshot())) return; await new Promise(done => setTimeout(done, 100)); }
    categorized('assertion');
  }
  const noProtected = (s: Awaited<ReturnType<typeof snapshot>>) => s.signin && s.rows.length === 0 && s.identity.length === 0 && f.expected.tenantADeviceIds.every(id => !s.body.includes(id));
  async function signedOut() { await until(noProtected); }
  async function element(selector: string) { const result = await action('/element', { using: 'css selector', value: selector }); const id = result?.['element-6066-11e4-a52e-4f735466cecf']; safeId(id); return id; }
  async function fill(selector: string, value: string) { const id = await element(selector); await action(`/element/${id}/clear`, {}); await action(`/element/${id}/value`, { text: value }); }
  async function click(selector: string) { const id = await element(selector); await action(`/element/${id}/click`, {}); }
  async function login(account: Account, bad = false) {
    diagnostic.actor = bad ? 'admin-bad' : account === f.accounts.admin ? 'admin' : account === f.accounts.operator ? 'operator' : account === f.accounts.owner ? 'owner' : 'other';
    mark('signin-assert', 'assertion');
    check(++attempts <= (phase === 'initial' ? 6 : 1)); await signedOut();
    mark('login-tenant'); await fill('input[name="tenantId"]', account.tenantId); mark('login-name'); await fill('input[name="loginName"]', account.loginName);
    mark('login-password'); await fill('input[name="password"]', bad ? account.password + '-invalid-synthetic' : account.password);
    mark('login-submit'); await click('form.login-panel button[type="submit"]');
    mark(bad ? 'bad-login-assert' : 'identity-assert', 'assertion');
    if (bad) await until(s => noProtected(s) && s.errors > 0);
    else await until(s => !s.signin && s.identity.length === 1);
  }
  async function devices(role: 'admin' | 'operator') { mark('devices-assert', 'assertion'); await until(s => !s.signin && s.identity[0] === role && s.errors === 0 && JSON.stringify([...s.rows].sort()) === JSON.stringify([...f.expected.tenantADeviceIds].sort())); }
  async function logout() {
    mark('logout-click'); await click('.identity button'); mark('logout-assert', 'assertion'); await signedOut();
    // A signed-out React view alone cannot prove the server invalidated its cookie.
    mark('logout-refresh'); await action('/refresh', {}); mark('logout-persisted-assert', 'assertion'); await signedOut();
  }
  if (phase === 'initial') {
    // Exclusive files/profile reject accidental reruns or reuse of a previous browser profile.
    mark('profile-create', 'filesystem'); mkdirSync(f.profilePath, { mode: 0o700 }); privatePath(f.profilePath, 'directory');
    mark('state-create', 'filesystem'); const fd = openSync(statePath, constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW, 0o600);
    try {
      mark('session-create'); const result = await request('POST', '/session', { capabilities: { alwaysMatch: { browserName: 'chrome', acceptInsecureCerts: false, pageLoadStrategy: 'eager', timeouts: { implicit: 0, pageLoad: 5000, script: 5000 }, 'goog:chromeOptions': { binary: f.chromeBinary, args: ['--headless=new', '--no-proxy-server', '--window-size=1280,900', '--disable-extensions', '--disable-background-networking', '--disable-sync', '--disable-component-update', '--disable-default-apps', '--no-first-run', '--no-default-browser-check', '--metrics-recording-only', '--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1', `--user-data-dir=${f.profilePath}`], prefs: { credentials_enable_service: false, 'profile.password_manager_enabled': false } } } } });
      mark('session-save'); safeId(result?.sessionId); sessionId = result.sessionId; writeFileSync(fd, JSON.stringify({ sessionId }));
    } finally { closeSync(fd); }
    mark('navigate'); await request('POST', path('/url'), { url: f.appOrigin + '/#/devices' }); mark('signin-assert', 'assertion'); await signedOut();
    await login(f.accounts.admin, true); await login(f.accounts.admin); await devices('admin'); await logout();
    await login(f.accounts.operator); await devices('operator'); await logout();
    await login(f.accounts.other); mark('tenant-empty-assert', 'assertion'); await until(s => !s.signin && s.rows.length === 0 && s.errors === 0 && s.empty.some(value => value.includes('Go 服务已返回空设备列表')) && f.expected.tenantADeviceIds.every(id => !s.body.includes(id))); await logout();
    await login(f.accounts.owner); mark('owner-denied-assert', 'assertion'); await until(s => !s.signin && s.identity[0] === 'owner' && s.rows.length === 0 && s.empty.some(value => value.includes('当前角色无设备读取权限')) && f.expected.tenantADeviceIds.every(id => !s.body.includes(id))); await logout();
    await login(f.accounts.admin); await devices('admin');
  } else {
    if (phase === 'cleanup') { try { lstatSync(statePath); } catch (error) { if ((error as NodeJS.ErrnoException).code === 'ENOENT') return; throw error; } }
    mark('state-read', 'filesystem'); privatePath(f.profilePath, 'directory');
    const saved = privateRead(statePath) as { sessionId: unknown }; check(saved && Object.keys(saved).length === 1); safeId(saved.sessionId); sessionId = saved.sessionId;
    if (phase === 'cleanup') { mark('session-delete'); await request('DELETE', path()); mark('state-delete', 'filesystem'); unlinkSync(statePath); return; }
    mark('existing-url'); await urlCheck();
    if (phase === 'gate-prepare' || phase === 'restart-prepare') { await signedOut(); await login(f.accounts.admin); await devices('admin'); }
    else { mark('reload'); await action('/refresh', {}); mark('revoked-assert', 'assertion'); await signedOut(); }
  }
}

let phase: Phase | 'selftest' | 'invalid' = 'invalid';
let timer: ReturnType<typeof setTimeout> | undefined;
try {
  const args = process.argv.slice(2);
  if (args.length === 1 && args[0] === '--selftest') { phase = 'selftest'; mark('selftest'); selftest(); }
  else {
    check(args.length === 4 && args[0] === '--phase' && phases.includes(args[1] as Phase) && args[2] === '--fixture'); phase = args[1] as Phase;
    timer = setTimeout(() => { diagnostic.category = 'timeout'; process.stdout.write(JSON.stringify(failureResult(phase)) + '\n'); process.exit(1); }, phase === 'initial' ? 45_000 : 20_000);
    await run(phase, args[3]);
  }
  if (timer) clearTimeout(timer); process.stdout.write(JSON.stringify({ phase, result: 'PASS' }) + '\n');
} catch (error) {
  if (timer) clearTimeout(timer); process.stdout.write(JSON.stringify(failureResult(phase, error)) + '\n'); process.exitCode = 1;
}
