/** Go migration is explicit; all other modes retain the Node prototype. */
export const isGoMode = (mode: string) => mode === 'go';
export function consoleProxy(mode: string) {
  return {
    target: isGoMode(mode) ? 'http://127.0.0.1:3101' : 'http://127.0.0.1:3100',
    changeOrigin: false,
    ws: !isGoMode(mode),
  };
}
