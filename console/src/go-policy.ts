import type { Role } from './types.ts';
export const canReadGoDevices = (role: Role) => role === 'admin' || role === 'operator';
export const isGoDeviceRoute = (route: string) => route === '/devices';
