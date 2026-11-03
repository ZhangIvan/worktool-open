import { ApiError, errorText } from './api.ts';
import type { api } from './api.ts';
import type { Auth } from './types.ts';

export const GO_REVALIDATE_MS = 15_000;
export type GoSessionState = { auth: Auth | null; checking: boolean; pending: boolean; error: string; notice: string; revision: number };

/** Owns session generations so obsolete responses and unmounted work cannot restore identity. */
export function createGoSession(request: typeof api, csrf: (value: string) => void, changed: (state: GoSessionState) => void) {
  let state: GoSessionState = { auth: null, checking: true, pending: false, error: '', notice: '', revision: 0 };
  let generation = 0;
  let disposed = false;
  let active: AbortController | null = null;
  const update = (patch: Partial<GoSessionState>) => { if (!disposed) { state = { ...state, ...patch }; changed(state); } };
  const cancel = () => { generation++; active?.abort(); active = null; };
  const clear = (notice: string) => { cancel(); csrf(''); update({ auth: null, checking: false, pending: false, notice, revision: state.revision + 1 }); };
  const expired = (now = Date.now()) => {
    const expiry = Date.parse(state.auth?.expiresAt ?? '');
    if (state.auth && Number.isFinite(expiry) && expiry <= now) { clear('会话已过期，请重新登录。'); return true; }
    return false;
  };
  async function run(kind: 'me' | 'login' | 'logout', body?: unknown) {
    if (disposed || state.pending || (kind === 'me' && active)) return false;
    if (kind === 'me' && expired()) return false;
    cancel(); const current = generation; const controller = new AbortController(); active = controller;
    if (kind !== 'me') update({ pending: true, error: '', notice: '' });
    const currentRequest = () => !disposed && !controller.signal.aborted && current === generation;
    try {
      const next = await request<Auth>(`/api/v1/auth/${kind}`, { signal: controller.signal, ...(kind === 'me' ? {} : { method: 'POST' as const, body }) });
      if (!currentRequest()) return false;
      if (kind === 'logout') { clear('已退出当前会话。'); return true; }
      if (!next || typeof next.expiresAt !== 'string' || !Number.isFinite(Date.parse(next.expiresAt)) ||
          typeof next.csrfToken !== 'string' || next.csrfToken.trim().length === 0 || next.csrfToken.length > 1024) {
        throw new ApiError(502, 'INVALID_RESPONSE', 'Go 会话响应缺少有效到期时间或 CSRF，无法确认会话。');
      }
      csrf(next.csrfToken); update({ auth: next, error: '', notice: '', revision: state.revision + 1 });
      if (expired()) return false;
      return true;
    } catch (e) {
      if (!currentRequest()) return false;
      // Revalidation failure removes protected data rather than retaining an unverified role.
      if (kind === 'me' || (e instanceof ApiError && (e.status === 401 || e.code === 'INVALID_RESPONSE'))) {
        csrf(''); update({ auth: null, revision: state.revision + 1 });
      }
      update({ error: e instanceof ApiError && e.status === 401 && kind === 'me' ? '' : errorText(e) });
      return false;
    } finally {
      if (currentRequest()) { active = null; update({ checking: false, pending: false }); }
    }
  }
  return {
    get state() { return state; },
    check: () => run('me'), login: (body: unknown) => run('login', body), logout: () => run('logout'),
    invalidate: () => { if (!disposed) clear('会话已失效，请重新登录。'); },
    expire: expired,
    dispose: () => { disposed = true; cancel(); },
  };
}
