import test from 'node:test';
import assert from 'node:assert/strict';
import { api, ApiError, setCsrf, utcExpiry, isUncertain } from './src/api.ts';
const events = new EventTarget();
Object.defineProperty(globalThis,'window',{value:events,configurable:true});
const originalFetch=globalThis.fetch;
test.after(()=>{globalThis.fetch=originalFetch;setCsrf('');});
test('same-origin write includes cookie CSRF and stable idempotency without changing payload',async()=>{
  setCsrf('synthetic-csrf-not-a-credential');let seen:RequestInit|undefined;
  globalThis.fetch=async(_input,init)=>{seen=init;return new Response(JSON.stringify({ok:true}),{status:200});};
  const result=await api<{ok:boolean}>('/api/v1/commands',{method:'POST',body:{payload:{text:'synthetic'}},key:'synthetic-intent'});
  assert.equal(result.ok,true);assert.equal(seen?.credentials,'include');assert.equal(seen?.cache,'no-store');
  assert.equal((seen?.headers as Record<string,string>)['X-CSRF-Token'],'synthetic-csrf-not-a-credential');assert.equal((seen?.headers as Record<string,string>)['Idempotency-Key'],'synthetic-intent');
  assert.deepEqual(JSON.parse(String(seen?.body)),{payload:{text:'synthetic'}});
});
test('non-local API path rejected before network',async()=>{
  let called=false;globalThis.fetch=async()=>{called=true;return new Response('{}');};
  await assert.rejects(api('https://example.invalid/api'));assert.equal(called,false);
});
test('empty 204 response is accepted',async()=>{
  globalThis.fetch=async()=>new Response(null,{status:204});assert.equal(await api('/api/v1/auth/logout',{method:'POST'}),null);
});
test('API errors preserve canonical code and requestId, not raw details',async()=>{
  globalThis.fetch=async()=>new Response(JSON.stringify({error:{code:'ROLE_DENIED',message:'Denied',requestId:'req_synthetic',details:{ignored:'not displayed'}}}),{status:403});
  await assert.rejects(api('/api/v1/commands',{method:'POST'}),(e:unknown)=>e instanceof ApiError&&e.code==='ROLE_DENIED'&&e.requestId==='req_synthetic'&&!e.message.includes('ignored'));
});
test('network failure is unconfirmed, never a fabricated success',async()=>{
  globalThis.fetch=async()=>{throw new TypeError('fetch failed');};
  await assert.rejects(api('/api/v1/commands',{method:'POST'}),(e:unknown)=>e instanceof ApiError&&isUncertain(e)&&e.code==='CONNECTION_UNCONFIRMED');
});
test('expiry matches protocol whole-second RFC3339 shape',()=>{
  const before=Date.now();const expiry=utcExpiry(120);assert.match(expiry,/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/);assert.ok(Date.parse(expiry)>=before+119000&&Date.parse(expiry)<=Date.now()+120000);
});
test('obsolete session response cannot invalidate a newer session',async()=>{
  let invalidations=0;const listener=()=>invalidations++;events.addEventListener('session-invalid',listener);
  setCsrf('old-synthetic-session');globalThis.fetch=async()=>{setCsrf('new-synthetic-session');return new Response(JSON.stringify({error:{code:'UNAUTHENTICATED',message:'Expired'}}),{status:401});};
  await assert.rejects(api('/api/v1/commands'));assert.equal(invalidations,0);events.removeEventListener('session-invalid',listener);
});

test('approved export is usable only by its requester on its approved device',async()=>{
  const {approvedForDevice}=await import('./src/logic.ts');
  const item={exportId:'exp_synthetic',requestedBy:'operator_a',scopeDigest:'a'.repeat(64),scope:{deviceId:'device_a',referenceIds:['synthetic_demo']},state:'approved',approvalId:'approval_synthetic'};
  assert.equal(approvedForDevice(item,'operator_a','device_a'),true);
  assert.equal(approvedForDevice(item,'operator_b','device_a'),false);
  assert.equal(approvedForDevice(item,'operator_a','device_b'),false);
  assert.equal(approvedForDevice({...item,scope:{referenceIds:['synthetic_demo']}},'operator_a','device_a'),false);
  assert.equal(approvedForDevice({...item,state:'pending'},'operator_a','device_a'),false);
});

test('malformed successful list is an error rather than an empty success',async()=>{
  globalThis.fetch=async()=>new Response(JSON.stringify({unexpected:[]}),{status:200});
  await assert.rejects(api('/api/v1/devices'),(e:unknown)=>e instanceof ApiError&&e.code==='INVALID_RESPONSE');
});

test('owner has no business writes or admin reads; only overview and directory routes',async()=>{
  const {can,canAccessRoute}=await import('./src/permissions.ts');
  const blocked=['devices.enroll','devices.revoke','commands.create','commands.cancel','exports.create','exports.decide','data.delete','audit.read','devices.read','commands.read'] as const;
  for(const action of blocked)assert.equal(can('owner',action),false,action);
  assert.equal(canAccessRoute('owner','/overview'),true);assert.equal(canAccessRoute('owner','/capabilities'),true);
  for(const route of ['/devices','/commands','/commands/new','/commands/cmd_other','/exports','/audit','/planned'])assert.equal(canAccessRoute('owner',route),false,route);
});
test('roles do not inherit business execution or approval powers',async()=>{
  const {can}=await import('./src/permissions.ts');
  assert.equal(can('admin','devices.enroll'),true);assert.equal(can('admin','commands.create'),false);assert.equal(can('admin','exports.decide'),false);
  assert.equal(can('operator','commands.create'),true);assert.equal(can('operator','exports.decide'),false);assert.equal(can('operator','devices.revoke'),false);
  assert.equal(can('approver','exports.decide'),true);assert.equal(can('approver','commands.create'),false);
  assert.equal(can('auditor','audit.read'),true);assert.equal(can('auditor','data.delete'),false);
});
test('operator command reads and cancel gates fail closed for another or missing requester',async()=>{
  const {commandInScope,canCancelCommand}=await import('./src/permissions.ts');
  const user={tenantId:'tenant_synthetic',principalId:'operator_a',role:'operator' as const};
  const own={requestedBy:'operator_a',commandId:'cmd_synthetic',deviceId:'device_synthetic',capability:'message.send_text',status:'queued'};
  assert.equal(commandInScope(user,own),true);assert.equal(canCancelCommand(user,own),true);
  for(const requestedBy of ['operator_b',undefined]){assert.equal(commandInScope(user,{...own,requestedBy}),false);assert.equal(canCancelCommand(user,{...own,requestedBy}),false);}
  assert.equal(canCancelCommand(user,{...own,status:'execution_unknown'}),false);assert.equal(canCancelCommand(user,{...own,status:'succeeded'}),false);
  assert.equal(canCancelCommand({...user,role:'owner'},own),false);
});
test('owner route guard performs no protected fetch',async()=>{
  const {canAccessRoute}=await import('./src/permissions.ts');let calls=0;
  globalThis.fetch=async()=>{calls++;return new Response(JSON.stringify({items:[]}));};
  for(const [route,endpoint] of [['/devices','/api/v1/devices'],['/commands','/api/v1/commands'],['/audit','/api/v1/audit']])if(canAccessRoute('owner',route))await api(endpoint);
  assert.equal(calls,0);
});
