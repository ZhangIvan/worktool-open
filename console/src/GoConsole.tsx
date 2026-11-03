import { useEffect, useRef, useState, type FormEvent } from 'react';
import { api, setCsrf } from './api';
import { useRead, useRoute } from './hooks';
import { canReadGoDevices, isGoDeviceRoute } from './go-policy';
import { createGoSession, GO_REVALIDATE_MS, type GoSessionState } from './go-session';
import type { Device, List } from './types';

const boundary = 'Go 迁移预览 · SYNTHETIC · 仅本地合成数据，未部署，无真实设备连接';

export default function GoConsole() {
  const [state, setState] = useState<GoSessionState>({ auth: null, checking: true, pending: false, error: '', notice: '', revision: 0 });
  const session = useRef<ReturnType<typeof createGoSession> | null>(null);
  const { auth, checking, pending, error, notice, revision } = state;
  const currentRoute = useRoute();
  const route = window.location.hash ? currentRoute : '/devices';

  useEffect(() => {
    const next = createGoSession(api, setCsrf, setState);
    session.current = next;
    const check = () => { void next.check(); };
    const invalid = () => next.invalidate();
    window.addEventListener('session-invalid', invalid);
    window.addEventListener('focus', check);
    const timer = window.setInterval(check, GO_REVALIDATE_MS);
    check();
    return () => {
      next.dispose(); session.current = null;
      clearInterval(timer);
      window.removeEventListener('focus', check);
      window.removeEventListener('session-invalid', invalid);
    };
  }, []);

  useEffect(() => {
    const expiry = Date.parse(auth?.expiresAt ?? '');
    if (!Number.isFinite(expiry)) return;
    const timer = window.setTimeout(() => session.current?.expire(), Math.max(0, Math.min(expiry - Date.now(), 2_147_483_647)));
    return () => clearTimeout(timer);
  }, [auth]);

  async function login(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const password = form.elements.namedItem('password') as HTMLInputElement;
    const data = new FormData(form);
    try {
      if (await session.current?.login({ tenantId: String(data.get('tenantId')).trim(), loginName: String(data.get('loginName')).trim(), password: String(data.get('password')) })) window.location.hash = '/devices';
    } finally { password.value = ''; }
  }
  const logout = () => session.current?.logout();

  if (checking) return <div className="boot"><p className="banner">{boundary}</p><p role="status">正在确认本地 Go 服务会话…</p></div>;
  if (!auth) return <div className="login-layout"><section className="login-intro"><p className="banner">Go · SYNTHETIC</p><h1>只读设备迁移预览</h1><p>{boundary}</p><p>仅登录、设备列表和退出已接通。命令、导出、审计和管理写入尚未迁移。</p></section><form className="login-panel" onSubmit={login}><h2>登录 Go 本地服务</h2>{error && <p className="error" role="alert">{error}</p>}{notice && <p role="status">{notice}</p>}<fieldset disabled={pending}><label>租户<input name="tenantId" required autoComplete="off" /></label><label>账号<input name="loginName" required autoComplete="username" /></label><label>密码<input name="password" type="password" required autoComplete="current-password" /></label><button className="primary wide" type="submit">{pending ? '登录中…' : '登录'}</button></fieldset><p className="fine">使用本地合成账号；不要输入生产凭据。服务未连接时不会显示成功。</p></form></div>;

  return <div className="shell"><aside><a className="brand" href="#/devices">WorkTool<span>Go · 只读迁移预览</span></a><nav aria-label="主导航"><a href="#/devices" aria-current={isGoDeviceRoute(route) ? 'page' : undefined}>模拟设备（只读）</a></nav><p className="sidebar-note">无命令执行<br/>无管理写入<br/>未部署</p></aside><div className="main"><header><span className="banner">Go · SYNTHETIC</span><div className="identity"><span>{auth.user.tenantId} / {auth.user.principalId}<strong>{auth.user.role}</strong></span><button disabled={pending} onClick={() => void logout()}>{pending ? '退出中…' : '退出'}</button></div></header><main><p className="notice">{boundary}</p><p>命令、导出、审计和设备管理写入尚未迁移（planned_unavailable）；此页面不报告它们的数量或完成状态。</p>{error && <p className="error" role="alert">{error}</p>}{isGoDeviceRoute(route) ? canReadGoDevices(auth.user.role) ? <GoDevices key={`${auth.user.tenantId}:${auth.user.principalId}:${auth.user.role}:${revision}`} /> : <p className="empty">当前角色无设备读取权限。仅 admin / operator 可读；owner 不继承设备权限。</p> : <p className="empty">此路由尚未迁移或不存在。<a href="#/devices">前往只读设备页</a></p>}</main><footer>状态仅来自 Go API 响应；合成记录不代表真实设备或生产部署。</footer></div></div>;
}

function GoDevices() {
  const result = useRead<List<Device>>('/api/v1/devices');
  return <section><div className="page-title"><h1>模拟设备（只读）</h1><button disabled={result.loading} onClick={result.refresh}>刷新</button></div>{result.loading ? <p role="status">正在读取设备…</p> : result.error ? <p className="error" role="alert">{result.error}</p> : result.data ? result.data.items.length === 0 ? <p className="empty">Go 服务已返回空设备列表。当前没有可显示的设备；不表示任何命令或部署已经完成。</p> : <div className="table-wrap"><table><thead><tr><th>设备</th><th>状态</th><th>策略版本</th><th>撤销版本</th></tr></thead><tbody>{result.data.items.map(device => <tr key={device.deviceId}><td>{device.name ?? device.deviceId}<code>{device.deviceId}</code></td><td>{device.state}</td><td>{device.policyVersion ?? '未提供'}</td><td>{device.revocationEpoch ?? '未提供'}</td></tr>)}</tbody></table></div> : <p role="status">尚未取得设备响应。</p>}</section>;
}
