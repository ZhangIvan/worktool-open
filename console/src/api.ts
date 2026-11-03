let csrfToken = '';
export function setCsrf(token: string) { csrfToken = token; }
export class ApiError extends Error {
  status: number; code: string; requestId?: string;
  constructor(status: number, code: string, message: string, requestId?: string) { super(message); this.status = status; this.code = code; this.requestId = requestId; }
}
export type RequestOptions = { method?: 'GET'|'POST'|'PUT'; body?: unknown; signal?: AbortSignal; key?: string };
export async function api<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  if (!path.startsWith('/api/v1/')) throw new Error('仅允许本部署 API');
  const method = opts.method ?? 'GET';
  const requestCsrf = csrfToken;
  const headers: Record<string,string> = { Accept: 'application/json' };
  if (method !== 'GET') { headers['Content-Type'] = 'application/json'; if (csrfToken) headers['X-CSRF-Token'] = csrfToken; if (opts.key) headers['Idempotency-Key'] = opts.key; }
  const controller = new AbortController();
  const abort = () => controller.abort(); opts.signal?.addEventListener('abort', abort, {once:true});
  if (opts.signal?.aborted) controller.abort();
  const timer = setTimeout(abort, 15000);
  try {
    const response = await fetch(path, { method, headers, credentials: 'include', cache: 'no-store', signal: controller.signal, ...(method !== 'GET' ? {body: JSON.stringify(opts.body ?? {})} : {}) });
    const value = response.status === 204 ? null : await response.json().catch(() => null);
    if (!response.ok) {
      if (!controller.signal.aborted && response.status === 401 && !path.endsWith('/auth/login') && requestCsrf === csrfToken) window.dispatchEvent(new Event('session-invalid'));
      const error = value?.error;
      if (!controller.signal.aborted && response.status === 403 && method !== 'GET' && requestCsrf === csrfToken) window.dispatchEvent(new CustomEvent('permissions-stale', {detail: String(error?.code ?? 'ROLE_DENIED')}));
      throw new ApiError(response.status, String(error?.code ?? 'HTTP_ERROR'), String(error?.message ?? `请求失败 (${response.status})`), error?.requestId);
    }
    if (response.status !== 204) {
      if (!value || typeof value !== 'object') throw new ApiError(502, 'INVALID_RESPONSE', '后台返回了不可解析的响应；结果未确认。');
      if (method === 'GET' && /^\/api\/v1\/(devices|commands|exports|audit)(?:\?|$)/.test(path) && !Array.isArray(value.items)) throw new ApiError(502, 'INVALID_RESPONSE', '列表响应缺少 items，未显示为成功或空列表。');
      if (/\/auth\/(login|me)$/.test(path) && (!value.user || typeof value.user.tenantId !== 'string' || typeof value.user.principalId !== 'string' || !['owner','admin','operator','approver','auditor'].includes(value.user.role) || typeof value.csrfToken !== 'string')) throw new ApiError(502, 'INVALID_RESPONSE', '会话响应不完整，无法建立已认证页面。');
    }
    return value as T;
  } catch (error) {
    if (error instanceof ApiError) throw error;
    if (opts.signal?.aborted) throw new DOMException('Aborted','AbortError');
    throw new ApiError(0, 'CONNECTION_UNCONFIRMED', method === 'GET' ? '无法读取服务，请确认本地后台可用后重试。' : '响应未确认。不要另建相同操作；重试将沿用当前意图，或先查询已有记录。');
  } finally { clearTimeout(timer); opts.signal?.removeEventListener('abort',abort); }
}
export function errorText(error: unknown): string { return error instanceof ApiError ? `${error.message} · ${error.code}${error.requestId ? ` · 请求 ${error.requestId}` : ''}` : '操作失败，请刷新后重试。'; }
export const isUncertain = (error: unknown) => error instanceof ApiError && (error.status === 0 || error.status >= 500);
export const utcExpiry = (seconds: number) => new Date(Date.now() + seconds * 1000).toISOString().replace(/\.\d{3}Z$/, 'Z');
export const newKey = () => crypto.randomUUID();
