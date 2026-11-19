import type { Command, Role, User } from './types';
/** UI gates are defensive presentation rules, never server authorization. */
export type Permission = 'overview.read'|'capabilities.read'|'planned.read'|'devices.read'|'devices.enroll'|'devices.revoke'|'commands.read'|'commands.create'|'commands.cancel'|'exports.read'|'exports.create'|'exports.decide'|'audit.read'|'data.delete';
const grants:Record<Role,readonly Permission[]> = {
  owner: ['overview.read','capabilities.read'],
  admin: ['overview.read','capabilities.read','planned.read','devices.read','devices.enroll','devices.revoke','commands.read','commands.cancel','audit.read','data.delete'],
  operator: ['overview.read','capabilities.read','planned.read','devices.read','commands.read','commands.create','commands.cancel','exports.read','exports.create'],
  approver: ['overview.read','capabilities.read','planned.read','exports.read','exports.decide'],
  auditor: ['overview.read','capabilities.read','planned.read','audit.read'],
};
export function can(role:Role, permission:Permission):boolean { return grants[role]?.includes(permission)??false; }
export function canAccessRoute(role:Role, route:string):boolean {
  const path=route.split('?')[0];
  if(path==='/overview')return can(role,'overview.read');
  if(path==='/capabilities')return can(role,'capabilities.read');
  if(path==='/planned')return can(role,'planned.read');
  if(path==='/devices')return can(role,'devices.read');
  if(path==='/commands/new')return can(role,'commands.create');
  if(path==='/commands'||path.startsWith('/commands/'))return can(role,'commands.read');
  if(path==='/exports')return can(role,'exports.read');
  if(path==='/audit')return can(role,'audit.read');
  return false;
}
export function commandInScope(user:User, command:Command):boolean {
  if(!can(user.role,'commands.read'))return false;
  if(user.role==='admin')return true; // tenant/object access remains server-controlled
  return user.role==='operator' && typeof command.requestedBy==='string' && command.requestedBy===user.principalId;
}
export function canCancelCommand(user:User, command:Command):boolean {
  return can(user.role,'commands.cancel') && commandInScope(user,command) && ['created','authorized','queued','delivered','accepted','running'].includes(command.status);
}
