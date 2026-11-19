# Synthetic device simulator

Node 24 native TypeScript, root-installed `ws`, `ajv`, `ajv-formats`, `canonicalize`; SQLite uses `node:sqlite`.
Only loopback self-hosted service URLs are accepted. No Android, real account, external domain, original APK, arbitrary filesystem export or remote shell is supported.

Programmatic use:
```ts
import { SyntheticDevice } from './device.ts';
const device = new SyntheticDevice({
  baseUrl: 'http://127.0.0.1:3000', dbPath: './simulator/demo.sqlite',
  deviceId, keyId, deviceSecret, // obtain through authorized synthetic enrollment; never log
  confirm: async () => false, // fail closed; explicit local synthetic consent required
});
await device.connect();
// await device.close() before process shutdown
```

The database contains synthetic inbox content: do not provide real personal data. `file.export` accepts only an in-memory configured exportId with matching approvalId/scopeDigest, `localConfirmed:true` and a positive per-action confirmation callback. It records a synthetic content hash, never reads a user-selected pathname. Configuration is programmatic so secrets do not appear in command-line process arguments.

`fault: 'drop_result'` persists the result but suppresses its first transmission; reconnect retransmits without re-execution. `fault: 'unknown_after_effect'` makes one modeled synthetic effect then reports uncertainty, never auto-replays. `beforeExecute` is a test barrier for stop/revoke races; not a remote-controlled script facility. `emergencyStop()` persists a local stop at the next execution check.

`node --test tests/*.test.ts` runs the integration suite after the service implementation and dependencies are ready. Report only observed runner results; Android/UI are NOT_RUN.
