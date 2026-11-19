import type { ExportRequest } from './types';
/** UX filtering only. The server must independently enforce all bindings. */
export function approvedForDevice(item:ExportRequest, principalId:string, deviceId:string):boolean {
  return !!deviceId && item.state==='approved' && item.requestedBy===principalId && !!item.approvalId && item.scope?.deviceId===deviceId;
}
