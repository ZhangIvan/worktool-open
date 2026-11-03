import assert from 'node:assert/strict';
import test from 'node:test';
import { createGoSession } from './src/go-session.ts';
import { api, ApiError, setCsrf } from './src/api.ts';
import { canReadGoDevices } from './src/go-policy.ts';
import type { Auth } from './src/types.ts';

const auth = (role: Auth['user']['role'] = 'operator', csrfToken = 'synthetic-a'): Auth => ({ user: { tenantId: 'synthetic', principalId: 'synthetic', role }, csrfToken, expiresAt: '2099-01-01T00:00:00Z' });
function fixture() {
  const calls: { path: string; signal?: AbortSignal; resolve: (auth: Auth) => void; reject: (e: unknown) => void }[] = [];
  let token = ''; let updates = 0;
  const request: typeof api = <T>(path: string, options: Parameters<typeof api>[1] = {}) => new Promise<T>((resolve, reject) => calls.push({ path, signal: options.signal, resolve: value => resolve(value as T), reject }));
  const session = createGoSession(request, value => { token = value; }, () => { updates++; });
  return { calls, session, token: () => token, updates: () => updates };
}

test('a newer login aborts and ignores old session validation responses', async () => {
  const f = fixture(); const old = f.session.check(); const login = f.session.login({});
  assert.equal(f.calls[0].signal?.aborted, true);
  f.calls[1].resolve(auth('admin', 'new')); await login;
  f.calls[0].resolve(auth('operator', 'old')); await old;
  assert.equal(f.token(), 'new'); assert.equal(f.session.state.auth?.user.role, 'admin');
});

test('role downgrade refresh removes device authorization; failed revalidation clears identity and CSRF', async () => {
  const f = fixture(); let pending = f.session.check(); f.calls[0].resolve(auth()); await pending;
  const revision = f.session.state.revision;
  pending = f.session.check(); f.calls[1].resolve(auth('owner')); await pending;
  assert.equal(canReadGoDevices(f.session.state.auth!.user.role), false);
  assert.ok(f.session.state.revision > revision);
  pending = f.session.check(); f.calls[2].reject(new ApiError(401, 'SESSION_INVALID', 'expired')); await pending;
  assert.equal(f.session.state.auth, null); assert.equal(f.token(), '');
});

test('repeated login/logout clicks are serialized and old checks cannot revive logout', async () => {
  const f = fixture(); const login = f.session.login({});
  assert.equal(await f.session.login({}), false); assert.equal(f.calls.length, 1);
  f.calls[0].resolve(auth()); await login;
  const old = f.session.check(); const logout = f.session.logout();
  assert.equal(f.calls[1].signal?.aborted, true);
  assert.equal(await f.session.logout(), false); assert.equal(f.calls.length, 3);
  f.calls[2].resolve(auth()); await logout;
  f.calls[1].resolve(auth()); await old;
  assert.equal(f.session.state.auth, null); assert.equal(f.token(), ''); assert.equal(f.session.state.pending, false);
  const again = f.session.login({}); f.calls[3].resolve(auth('admin', 'again')); await again;
  assert.equal(f.token(), 'again');
});

test('known expiry and invalidation remove identity; disposal aborts without later updates', async () => {
  const f = fixture(); const login = f.session.login({}); f.calls[0].resolve(auth()); await login;
  assert.equal(f.session.expire(Date.parse('2099-01-01T00:00:00Z')), true);
  assert.equal(f.session.state.auth, null); assert.equal(f.token(), '');
  const pending = f.session.check(); f.session.dispose(); const updates = f.updates();
  assert.equal(f.calls[1].signal?.aborted, true);
  f.calls[1].resolve(auth()); await pending;
  assert.equal(f.updates(), updates); assert.equal(f.session.state.auth, null);
});

test('overlapping focus/timer checks are bounded and unconfirmed validation fails closed', async () => {
  const f = fixture(); const initial = f.session.check();
  assert.equal(await f.session.check(), false); assert.equal(f.calls.length, 1);
  f.calls[0].resolve(auth()); await initial;
  const check = f.session.check(); f.calls[1].reject(new ApiError(0, 'CONNECTION_UNCONFIRMED', 'unconfirmed')); await check;
  assert.equal(f.session.state.auth, null); assert.equal(f.token(), ''); assert.match(f.session.state.error, /unconfirmed/);
});

test('an aborted old fetch cannot emit session-invalid even when transport returns a late 401', async () => {
  const oldFetch = globalThis.fetch; const oldWindow = globalThis.window;
  let events = 0; let resolve!: (response: Response) => void;
  globalThis.window = { dispatchEvent: () => { events++; return true; } } as unknown as Window & typeof globalThis;
  globalThis.fetch = () => new Promise<Response>(done => { resolve = done; });
  try {
    setCsrf('same-token'); const controller = new AbortController();
    const request = api('/api/v1/devices', { signal: controller.signal }); controller.abort();
    resolve(new Response(JSON.stringify({ error: { code: 'SESSION_INVALID' } }), { status: 401 }));
    await assert.rejects(request); assert.equal(events, 0);
  } finally { globalThis.fetch = oldFetch; globalThis.window = oldWindow; setCsrf(''); }
});

test('Go login/me reject missing or invalid expiry and missing, empty, or unbounded CSRF before accepting identity', async () => {
  const malformed: Partial<Auth>[] = [
    { expiresAt: undefined }, { expiresAt: '' }, { expiresAt: 'not-a-date' },
    { csrfToken: undefined }, { csrfToken: '' }, { csrfToken: '   ' }, { csrfToken: 'x'.repeat(1025) },
  ];
  for (const kind of ['login', 'me'] as const) {
    for (const patch of malformed) {
      const f = fixture(); const initial = f.session.login({}); f.calls[0].resolve(auth()); await initial;
      const request = kind === 'login' ? f.session.login({}) : f.session.check();
      f.calls[1].resolve({ ...auth(), ...patch });
      assert.equal(await request, false, `${kind}: ${JSON.stringify(patch)}`);
      assert.equal(f.session.state.auth, null); assert.equal(f.token(), '');
      assert.match(f.session.state.error, /INVALID_RESPONSE/);
      assert.equal(f.session.state.pending, false); assert.equal(f.session.state.checking, false);
    }
  }
});

test('Go accepts bounded nonempty synthetic CSRF without imposing a token encoding', async () => {
  const f = fixture(); const login = f.session.login({});
  f.calls[0].resolve(auth('operator', 'synthetic:test/token+value='));
  assert.equal(await login, true); assert.equal(f.token(), 'synthetic:test/token+value=');
});
