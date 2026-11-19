import { test } from 'node:test';
import { DatabaseSync } from 'node:sqlite';
import { randomBytes } from 'node:crypto';
import assert from 'node:assert/strict';
import { mkdtempSync, rmSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createApp } from '../server/app.ts';
import { SyntheticDevice } from '../simulator/device.ts';
const password=randomBytes(24).toString('base64url');
const clock=()=>Date.parse('2030-01-01T00:00:00Z');
const origin='http://127.0.0.1:18888';
const bootstrap=[{tenantId:'tenant_demo',users:[
 {principalId:'admin_demo',loginName:'admin_demo',password,role:'admin'},
 {principalId:'owner_readonly',loginName:'owner_readonly',password,role:'owner'},
 {principalId:'operator_demo',loginName:'operator_demo',password,role:'operator'},
 {principalId:'operator_second',loginName:'operator_second',password,role:'operator'},
 {principalId:'approver_demo',loginName:'approver_demo',password,role:'approver'},
 {principalId:'auditor_demo',loginName:'auditor_demo',password,role:'auditor'}]},
 {tenantId:'tenant_other',users:[{principalId:'admin_other',loginName:'admin_other',password,role:'admin'}]}] as any;
async function until(fn:()=>Promise<any>|any,pred:(x:any)=>boolean,ms=4000){const start=Date.now();let value;do{value=await fn();if(pred(value))return value;await new Promise(r=>setTimeout(r,20));}while(Date.now()-start<ms);assert.fail('condition timeout: '+JSON.stringify(value));}
async function setup(){
 const dir=mkdtempSync(fileURLToPath(new URL('./runtime_',import.meta.url)));
 let app=await createApp({dbPath:join(dir,'server.sqlite'),clock,bootstrap,origin});
 let baseUrl=await app.listen({host:'127.0.0.1',port:0});
 const devices:SyntheticDevice[]=[];
 async function login(loginName='admin_demo',tenantId='tenant_demo'){
  const response=await app.inject({method:'POST',url:'/api/v1/auth/login',headers:{origin},payload:{tenantId,loginName,password}});
  assert.equal(response.statusCode,200,response.body);
  return {cookie:String(response.headers['set-cookie']).split(';')[0],csrf:response.json().csrfToken};
 }
 async function api(auth:any,method:any,url:string,payload?:any,extra:any={}){
  const response=await app.inject({method,url,headers:{origin,cookie:auth?.cookie??'','x-csrf-token':auth?.csrf??'',...extra},...(payload===undefined?{}:{payload})});
  return {status:response.statusCode,body:response.body?response.json():null};
 }
 const admin=await login(); const operator=await login('operator_demo');
 async function enroll(){
  const e=await api(admin,'POST','/api/v1/enrollment-sessions',{deviceLabel:'Synthetic device',capabilities:['message.send_text','file.export'],expiresInSeconds:300});assert.equal(e.status,201,JSON.stringify(e.body));
  const d=await api(null,'POST','/api/v1/devices/enroll',{enrollmentId:e.body.enrollmentId,enrollmentToken:e.body.enrollmentToken,deviceLabel:'Synthetic device',localConfirmed:true});assert.equal(d.status,201,JSON.stringify(d.body));return d.body;
 }
 const credentials=await enroll();let count=0;
 async function device(extra:any={}){const d=new SyntheticDevice({baseUrl,...credentials,dbPath:join(dir,'device.sqlite'),clock,...extra});devices.push(d);await d.connect();return d;}
 async function command(extra:any={},key?:string,auth=operator){return api(auth,'POST',`/api/v1/devices/${credentials.deviceId}/commands`,{deviceId:credentials.deviceId,capability:'message.send_text',payload:{targetId:'target_demo',text:'Synthetic hello'},expiresAt:'2030-01-01T00:02:00Z',requiresLocalConfirmation:false,...extra},{'idempotency-key':key??'idem_key_'+(++count)});}
 const state=(id:string)=>api(admin,'GET','/api/v1/commands/'+id).then(x=>x.body);
 async function cleanup(){for(const d of devices){try{await d.close();}catch{}}await app.close();rmSync(dir,{recursive:true,force:true});}
 async function restartServer(){for(const d of devices){try{await d.close();}catch{}}devices.length=0;await app.close();app=await createApp({dbPath:join(dir,'server.sqlite'),clock,bootstrap,origin});baseUrl=await app.listen({host:'127.0.0.1',port:0});Object.assign(admin,await login());Object.assign(operator,await login('operator_demo'));}
 return {enroll,restartServer,app,dir,admin,operator,login,api,credentials,device,command,state,cleanup,baseUrl};
}

test('real HTTP/WS synthetic text + idempotency + RBAC/tenant + logout',async()=>{
 const s=await setup();try{
 const d=await s.device();const a=await s.command({},'stable_key');assert.equal(a.status,202,JSON.stringify(a));
 const cid=a.body.commandId;await until(()=>s.state(cid),x=>x.status==='succeeded');assert.equal(d.inbox().length,1);
 const dup=await s.command({},'stable_key');assert.ok([200,201,202].includes(dup.status));assert.equal(dup.body.commandId,cid);assert.equal(d.inbox().length,1);
 const change=await s.command({payload:{targetId:'target_demo',text:'Changed'}},'stable_key');assert.equal(change.status,409);
 const auditor=await s.login('auditor_demo');assert.equal((await s.command({},undefined,auditor)).status,403);
 const other=await s.login('admin_other','tenant_other');assert.equal((await s.api(other,'GET','/api/v1/commands/'+cid)).status,404);
 assert.equal((await s.api(s.admin,'POST','/api/v1/auth/logout',{})).status,204);assert.equal((await s.api(s.admin,'GET','/api/v1/auth/me')).status,401);
 }finally{await s.cleanup();}
});

test('queued cancellation has no device effect',async()=>{
 const s=await setup();try{const c=await s.command();assert.equal(c.status,202);const cancel=await s.api(s.admin,'POST',`/api/v1/commands/${c.body.commandId}/cancel`,{});assert.equal(cancel.status,200,JSON.stringify(cancel));const d=await s.device();assert.equal((await s.state(c.body.commandId)).status,'cancelled');assert.equal(d.inbox().length,0);}finally{await s.cleanup();}
});

test('dropped result persists and reconnect reconciles without replay',async()=>{
 const s=await setup();try{const d=await s.device({fault:'drop_result'});const c=await s.command();await until(()=>d.inbox(),x=>x.length===1);await d.close();
 await until(()=>s.state(c.body.commandId),x=>x.status==='execution_unknown');
 const restarted=await s.device();await until(()=>s.state(c.body.commandId),x=>x.status==='succeeded');assert.equal(restarted.inbox().length,1);
 }finally{await s.cleanup();}
});

test('modeled unknown after effect never replays on reconnect',async()=>{
 const s=await setup();try{const d=await s.device({fault:'unknown_after_effect'});const c=await s.command();await until(()=>s.state(c.body.commandId),x=>x.status==='execution_unknown');assert.equal(d.inbox().length,1);await d.close();const restart=await s.device();await new Promise(r=>setTimeout(r,100));assert.equal(restart.inbox().length,1);assert.equal((await s.state(c.body.commandId)).status,'execution_unknown');}finally{await s.cleanup();}
});

test('export dual approval, wrong scope, and explicit local confirmation',async()=>{
 const s=await setup();try{
 const req=await s.api(s.operator,'POST','/api/v1/exports',{scope:{deviceId:s.credentials.deviceId,referenceIds:['synthetic_demo']},reason:'Synthetic export test',expiresInSeconds:300});assert.equal(req.status,201,JSON.stringify(req));
 const path=`/api/v1/exports/${req.body.exportId}/decisions`;
 assert.equal((await s.api(s.operator,'POST',path,{decision:'approved',scopeDigest:req.body.scopeDigest})).status,403);
 const approver=await s.login('approver_demo');assert.equal((await s.api(approver,'POST',path,{decision:'approved',scopeDigest:'b'.repeat(64)})).status,409);
 const approved=await s.api(approver,'POST',path,{decision:'approved',scopeDigest:req.body.scopeDigest});assert.equal(approved.status,200,JSON.stringify(approved));
 const d=await s.device({confirm:async()=>true,exports:{[req.body.exportId]:{approvalId:approved.body.approvalId,scopeDigest:req.body.scopeDigest,localConfirmed:true,syntheticContent:'Synthetic only'}}});
 const c=await s.command({capability:'file.export',payload:{exportId:req.body.exportId,approvalId:approved.body.approvalId,scopeDigest:req.body.scopeDigest},requiresLocalConfirmation:true});assert.equal(c.status,202,JSON.stringify(c));await until(()=>s.state(c.body.commandId),x=>x.status==='succeeded');assert.equal(d.inbox().length,0);
 }finally{await s.cleanup();}
});

test('stop_request at pre-effect safe point cancels without effect',async()=>{
 const s=await setup();let release!:()=>void;const barrier=new Promise<void>(r=>release=r);try{const d=await s.device({beforeExecute:async()=>barrier});const c=await s.command();await until(()=>s.state(c.body.commandId),x=>x.status==='accepted');await s.api(s.admin,'POST',`/api/v1/commands/${c.body.commandId}/cancel`,{});await new Promise(r=>setTimeout(r,30));release();await until(()=>s.state(c.body.commandId),x=>x.status==='cancelled');assert.equal(d.inbox().length,0);}finally{release();await s.cleanup();}
});

test('server and device restart preserve completed command and inbox',async()=>{
 const s=await setup();try{const d=await s.device();const c=await s.command({},'restart_idempotency');await until(()=>s.state(c.body.commandId),x=>x.status==='succeeded');assert.equal(d.inbox().length,1);await s.restartServer();const next=await s.device();assert.equal((await s.state(c.body.commandId)).status,'succeeded');const again=await s.command({},'restart_idempotency');assert.equal(again.body.commandId,c.body.commandId);assert.equal(next.inbox().length,1);}finally{await s.cleanup();}
});

test('device credential revocation blocks new sessions and stops in-flight before effect',async()=>{
 const s=await setup();let release!:()=>void;const barrier=new Promise<void>(r=>release=r);
 try{const d=await s.device({beforeExecute:async()=>barrier});const c=await s.command();await until(()=>s.state(c.body.commandId),x=>x.status==='accepted');const revoked=await s.api(s.admin,'POST',`/api/v1/devices/${s.credentials.deviceId}/revoke`,{expectedEpoch:0});assert.equal(revoked.status,200);await new Promise(r=>setTimeout(r,40));release();await d.chain;assert.equal(d.inbox().length,0);await assert.rejects(()=>s.device(),/SESSION_REJECTED/);await until(()=>s.state(c.body.commandId),x=>x.status==='execution_unknown');
 }finally{release();await s.cleanup();}
});

test('local confirmation missing rejects synthetic export before effect',async()=>{
 const s=await setup();try{const e=await s.api(s.operator,'POST','/api/v1/exports',{scope:{deviceId:s.credentials.deviceId,referenceIds:['synthetic_demo']},reason:'Synthetic only',expiresInSeconds:300});const approver=await s.login('approver_demo');const a=await s.api(approver,'POST',`/api/v1/exports/${e.body.exportId}/decisions`,{decision:'approved',scopeDigest:e.body.scopeDigest});const d=await s.device({confirm:async()=>false,exports:{[e.body.exportId]:{approvalId:a.body.approvalId,scopeDigest:e.body.scopeDigest,localConfirmed:true,syntheticContent:'Synthetic'}}});const c=await s.command({capability:'file.export',requiresLocalConfirmation:true,payload:{exportId:e.body.exportId,approvalId:a.body.approvalId,scopeDigest:e.body.scopeDigest}});await until(()=>s.state(c.body.commandId),x=>x.status==='rejected');assert.equal(d.db.prepare('SELECT COUNT(*) AS n FROM exports').get()!.n,0);}finally{await s.cleanup();}
});

test('approved export cannot be borrowed by a different operator or moved to another device',async()=>{
 const s=await setup();try{
 const request=await s.api(s.operator,'POST','/api/v1/exports',{scope:{deviceId:s.credentials.deviceId,referenceIds:['synthetic_demo']},reason:'Scoped synthetic export',expiresInSeconds:300});assert.equal(request.status,201);
 const approver=await s.login('approver_demo');const approved=await s.api(approver,'POST',`/api/v1/exports/${request.body.exportId}/decisions`,{decision:'approved',scopeDigest:request.body.scopeDigest});assert.equal(approved.status,200);
 const payload={exportId:request.body.exportId,approvalId:approved.body.approvalId,scopeDigest:request.body.scopeDigest};
 const anotherOperator=await s.login('operator_second');const borrowed=await s.command({capability:'file.export',payload,requiresLocalConfirmation:true},'borrow_approval',anotherOperator);assert.equal(borrowed.status,403);assert.equal(borrowed.body.error.code,'APPROVAL_REQUIRED');
 const second=await s.enroll();const moved=await s.api(s.operator,'POST',`/api/v1/devices/${second.deviceId}/commands`,{deviceId:second.deviceId,capability:'file.export',payload,requiresLocalConfirmation:true,expiresAt:'2030-01-01T00:02:00Z'},{'idempotency-key':'move_approval_device'});assert.equal(moved.status,403);assert.equal(moved.body.error.code,'APPROVAL_REQUIRED');
 const listing=await s.api(s.admin,'GET','/api/v1/commands');assert.equal(listing.body.items.length,0,'denied attempts create no commands');
 }finally{await s.cleanup();}
});

function deactivate(s:any,principalId:string){
 const db=new DatabaseSync(join(s.dir,'server.sqlite'));
 try{assert.equal(db.prepare('UPDATE users SET active=0 WHERE tenant=? AND id=?').run('tenant_demo',principalId).changes,1);}finally{db.close();}
}

test('queued command loses authorization when submitting operator is deactivated',async()=>{
 const s=await setup();try{
 const c=await s.command();assert.equal(c.status,202);assert.equal((await s.state(c.body.commandId)).status,'queued');
 deactivate(s,'operator_demo');const d=await s.device();
 await until(()=>s.state(c.body.commandId),x=>x.status==='cancelled');assert.equal(d.inbox().length,0);assert.equal(d.journal().length,0,'server must not deliver revoked queued command');
 assert.equal((await s.api(s.operator,'GET','/api/v1/commands')).status,401);
 }finally{await s.cleanup();}
});

test('deactivated approver invalidates both queued export and new submissions',async()=>{
 const s=await setup();try{
 const e=await s.api(s.operator,'POST','/api/v1/exports',{scope:{deviceId:s.credentials.deviceId,referenceIds:['synthetic_demo']},reason:'Synthetic active approver guard',expiresInSeconds:300});assert.equal(e.status,201);
 const approver=await s.login('approver_demo');const a=await s.api(approver,'POST',`/api/v1/exports/${e.body.exportId}/decisions`,{decision:'approved',scopeDigest:e.body.scopeDigest});assert.equal(a.status,200);
 const args={capability:'file.export',requiresLocalConfirmation:true,payload:{exportId:e.body.exportId,approvalId:a.body.approvalId,scopeDigest:e.body.scopeDigest}};
 const queued=await s.command(args);assert.equal(queued.status,202);deactivate(s,'approver_demo');
 const blocked=await s.command(args);assert.equal(blocked.status,403);assert.equal(blocked.body.error.code,'APPROVAL_REQUIRED');
 const d=await s.device({confirm:async()=>true,exports:{[e.body.exportId]:{approvalId:a.body.approvalId,scopeDigest:e.body.scopeDigest,localConfirmed:true,syntheticContent:'Synthetic'}}});
 await until(()=>s.state(queued.body.commandId),x=>x.status==='cancelled');assert.equal(d.db.prepare('SELECT COUNT(*) AS n FROM exports').get()!.n,0);assert.equal(d.journal().length,0);
 }finally{await s.cleanup();}
});

test('operator object scope restricts GET list and cancellation of another operators command',async()=>{
 const s=await setup();try{
 const c=await s.command();assert.equal(c.status,202);const second=await s.login('operator_second');
 assert.equal((await s.api(second,'GET',`/api/v1/commands/${c.body.commandId}`)).status,404);
 const list=await s.api(second,'GET','/api/v1/commands');assert.equal(list.status,200);assert.ok(!list.body.items.some((x:any)=>x.commandId===c.body.commandId));
 assert.equal((await s.api(second,'POST',`/api/v1/commands/${c.body.commandId}/cancel`,{})).status,404);
 assert.equal((await s.state(c.body.commandId)).status,'queued');
 const own=await s.api(s.operator,'GET',`/api/v1/commands/${c.body.commandId}`);assert.equal(own.status,200);
 assert.equal((await s.api(s.operator,'POST',`/api/v1/commands/${c.body.commandId}/cancel`,{})).status,200);
 }finally{await s.cleanup();}
});

test('owner is read-only in first batch and cannot enroll revoke or read audit',async()=>{
 const s=await setup();try{
 const owner=await s.login('owner_readonly');
 assert.equal((await s.api(owner,'GET','/api/v1/devices')).status,403);
 assert.equal((await s.api(owner,'POST','/api/v1/enrollment-sessions',{deviceLabel:'Forbidden synthetic enrollment',capabilities:['message.send_text'],expiresInSeconds:300})).status,403);
 assert.equal((await s.api(owner,'POST',`/api/v1/devices/${s.credentials.deviceId}/revoke`,{expectedEpoch:0})).status,403);
 assert.equal((await s.api(owner,'GET','/api/v1/audit')).status,403);
 assert.equal((await s.command({},'owner_cannot_execute',owner)).status,403);
 const devices=await s.api(s.admin,'GET','/api/v1/devices');assert.equal(devices.body.items.find((d:any)=>d.deviceId===s.credentials.deviceId).state,'active');
 }finally{await s.cleanup();}
});
