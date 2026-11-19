import {test} from 'node:test';
import assert from 'node:assert/strict';
import {randomBytes} from 'node:crypto';
import {mkdtempSync,rmSync} from 'node:fs';
import {fileURLToPath} from 'node:url';
import {join} from 'node:path';
import {createServer} from 'node:http';
import {DatabaseSync} from 'node:sqlite';
import {createApp} from '../server/app.ts';
import {SyntheticDevice} from '../simulator/device.ts';
import {verifyCallback,receiverURL} from '../server/developer/callbacks.ts';
const prefix='/api/developer/v1';
async function until(fn:()=>any,predicate:(v:any)=>boolean){for(let i=0;i<250;i++){const v=await fn();if(predicate(v))return v;await new Promise(r=>setTimeout(r,30));}assert.fail('timeout');}
async function setup(receiver?:string){
 const dir=mkdtempSync(fileURLToPath(new URL('./developer-runtime_',import.meta.url)));const password=randomBytes(24).toString('hex');const origin='http://127.0.0.1:18888';
 const app=await createApp({dbPath:join(dir,'server.sqlite'),origin,developer:{syntheticReceiverOrigin:receiver},bootstrap:[{tenantId:'tenant_test',users:[{principalId:'admin_test',loginName:'admin',role:'admin',password},{principalId:'owner_test',loginName:'owner',role:'owner',password}]},{tenantId:'tenant_other',users:[{principalId:'admin_other',loginName:'admin',role:'admin',password}]}]});const baseUrl=await app.listen({host:'127.0.0.1',port:0});
 const login=async(loginName='admin',tenantId='tenant_test')=>{const r=await app.inject({method:'POST',url:'/api/v1/auth/login',headers:{origin},payload:{loginName,tenantId,password}});assert.equal(r.statusCode,200);return {origin,cookie:String(r.headers['set-cookie']).split(';')[0],'x-csrf-token':r.json().csrfToken};};const admin=await login();
 const api=async(headers:any,method:any,url:string,payload?:any)=>{const r=await app.inject({method,url,headers,...(payload===undefined?{}:{payload})});return {status:r.statusCode,body:r.body?r.json():null};};
 const e=await api(admin,'POST','/api/v1/enrollment-sessions',{deviceLabel:'synthetic',capabilities:['message.send_text'],expiresInSeconds:300});const d=await api({},'POST','/api/v1/devices/enroll',{enrollmentId:e.body.enrollmentId,enrollmentToken:e.body.enrollmentToken,deviceLabel:'synthetic',localConfirmed:true});
 const makeApp=()=>api(admin,'POST',prefix+'/applications',{name:'synthetic app',deviceIds:[d.body.deviceId],scopes:['commands:write','results:read','callbacks:read','devices:read']});
 const a=await makeApp();assert.equal(a.status,201,JSON.stringify(a));let headers={authorization:'Bearer '+a.body.token};const devices:SyntheticDevice[]=[];
 const message=(key='stable_key',extra:any={})=>api({...headers,'idempotency-key':key},'POST',prefix+'/messages',{type:203,deviceId:d.body.deviceId,targetId:'synthetic_demo',text:'synthetic text',expiresInSeconds:120,...extra});
 const connect=async()=>{const dev=new SyntheticDevice({baseUrl,...d.body,dbPath:join(dir,'device.sqlite')});devices.push(dev);await dev.connect();return dev;};
 const cleanup=async()=>{for(const d of devices)await d.close();await app.close();rmSync(dir,{recursive:true,force:true});};
 return {app,api,admin,login,makeApp,a:a.body,headers,message,connect,cleanup,dir,device:d.body};
}
test('developer app scope/idempotency/rotation/owner isolation + real synthetic WS execution',async()=>{
 const s=await setup();try{
 assert.equal((await s.message('newline_target',{targetId:'synthetic_demo\n'})).status,400);
 assert.equal((await s.message('key_with\tcontrol')).status,400);
 assert.equal((await s.api({authorization:'Bearer '+s.a.token+'\n'},'GET',prefix+'/devices')).status,401);
 const a=await s.message();assert.equal(a.status,202,JSON.stringify(a));assert.equal(a.body.accepted,true);assert.equal(a.body.children.length,1);
 const dup=await s.message();assert.equal(dup.body.messageId,a.body.messageId);assert.equal((await s.message('stable_key',{text:'changed'})).status,409);
 const second=await s.makeApp();assert.equal((await s.api({authorization:'Bearer '+second.body.token},'GET',prefix+'/messages/'+a.body.messageId)).status,404);
 const owner=await s.login('owner');assert.equal((await s.api(owner,'POST',prefix+'/applications',{name:'bad',scopes:['commands:write'],deviceIds:[s.device.deviceId]})).status,403);
 const other=await s.login('admin','tenant_other');assert.equal((await s.api(other,'POST',prefix+'/applications',{name:'bad',scopes:['commands:write'],deviceIds:[s.device.deviceId]})).status,404);
 assert.equal((await s.message('unsupported',{type:218})).status,501);
 const dev=await s.connect();await until(()=>s.api(s.headers,'GET',prefix+'/messages/'+a.body.messageId),r=>r.body.children[0].status==='succeeded');assert.equal(dev.inbox().length,1);
 const rotated=await s.api(s.admin,'POST',prefix+'/applications/'+s.a.appId+'/rotate',{});assert.equal(rotated.status,200);assert.equal((await s.message('after_rotate')).status,401);
 const db=new DatabaseSync(join(s.dir,'server.sqlite'));const row=db.prepare('SELECT token_hash FROM developer_apps WHERE id=?').get(s.a.appId) as any;assert.notEqual(row.token_hash,rotated.body.token);db.close();
 }finally{await s.cleanup();}
});
test('revocation and unbinding rechecked before queued dispatch',async()=>{
 for(const operation of ['revoke','bindings']){const s=await setup();try{const a=await s.message();assert.equal(a.status,202);const r=await s.api(s.admin,operation==='bindings'?'PUT':'POST',prefix+'/applications/'+s.a.appId+'/'+operation,operation==='bindings'?{deviceIds:[]}:{});assert.equal(r.status,200);const dev=await s.connect();await until(()=>s.api(s.admin,'GET','/api/v1/commands/'+a.body.children[0].commandId),x=>x.body.status==='cancelled');assert.equal(dev.inbox().length,0);}finally{await s.cleanup();}}
});
test('callback defaults deny SSRF; signature tamper/replay rejected',async()=>{
 const s=await setup();try{for(const url of ['http://127.0.0.1:9/','https://example.invalid/','http://169.254.169.254/'])assert.equal((await s.api(s.admin,'POST',prefix+'/applications/'+s.a.appId+'/callback',{url})).status,501);assert.throws(()=>receiverURL('http://localhost:9/','http://127.0.0.1:9'));assert.throws(()=>receiverURL('http://127.0.0.1:10/','http://127.0.0.1:9'));}finally{await s.cleanup();}
});
test('transactional signed callback retries once and app-scoped delivery query',async()=>{
 const received:any[]=[];const receiver=createServer((req,res)=>{let body='';req.on('data',x=>body+=x);req.on('end',()=>{received.push({body,headers:req.headers});res.writeHead(received.length===1?503:200);res.end();});});await new Promise<void>(r=>receiver.listen(0,'127.0.0.1',r));const origin='http://127.0.0.1:'+(receiver.address() as any).port;const s=await setup(origin);
 try{const configured=await s.api(s.admin,'POST',prefix+'/applications/'+s.a.appId+'/callback',{url:origin+'/callback'});assert.equal(configured.status,200);const key=Buffer.from(configured.body.callbackSecret,'hex');await s.connect();const m=await s.message();assert.equal(m.status,202);await until(()=>s.api(s.headers,'GET',prefix+'/deliveries'),x=>x.body.items[0]?.state==='delivered');assert.equal(received.length,2);assert.equal(received[0].body,received[1].body);const r=received[1],h=r.headers;assert.ok(verifyCallback(key,h['x-worktool-event-id'],h['x-worktool-timestamp'],r.body,h['x-worktool-signature']));assert.equal(verifyCallback(key,h['x-worktool-event-id'],h['x-worktool-timestamp'],r.body+' ',h['x-worktool-signature']),false);assert.equal(verifyCallback(key,h['x-worktool-event-id'],h['x-worktool-timestamp'],r.body,h['x-worktool-signature'],Date.now()+60000),false);assert.ok(!r.body.includes('synthetic text'));
 const second=await s.makeApp();assert.deepEqual((await s.api({authorization:'Bearer '+second.body.token},'GET',prefix+'/deliveries')).body.items,[]);
 }finally{await s.cleanup();await new Promise<void>(r=>receiver.close(()=>r()));}
});
test('app scope denial and token expiry are independent of device ID knowledge',async()=>{
 const s=await setup();try{
 const db=new DatabaseSync(join(s.dir,'server.sqlite'));
 db.prepare('UPDATE developer_apps SET scopes=? WHERE id=?').run(JSON.stringify(['results:read']),s.a.appId);
 assert.equal((await s.message()).status,403);
 db.prepare('UPDATE developer_apps SET token_expires=0 WHERE id=?').run(s.a.appId);
 assert.equal((await s.api(s.headers,'GET',prefix+'/messages/message_missing')).status,401);db.close();
 }finally{await s.cleanup();}
});
test('callbacks do not follow redirects and bounded failures become deadletter',async()=>{
 let received=0;const receiver=createServer((_req,res)=>{received++;res.writeHead(302,{location:'http://example.invalid/never-contact'});res.end();});await new Promise<void>(r=>receiver.listen(0,'127.0.0.1',r));const origin='http://127.0.0.1:'+(receiver.address() as any).port;const s=await setup(origin);
 try{assert.equal((await s.api(s.admin,'POST',prefix+'/applications/'+s.a.appId+'/callback',{url:origin+'/redirect'})).status,200);await s.connect();await s.message();
 const db=new DatabaseSync(join(s.dir,'server.sqlite'));
 const result=await until(async()=>{db.prepare("UPDATE developer_callbacks SET next_at=0 WHERE state='pending'").run();return s.api(s.headers,'GET',prefix+'/deliveries');},x=>x.body.items[0]?.state==='deadletter');
 assert.equal(result.body.items[0].attempts,4);assert.equal(received,4);assert.equal(result.body.items[0].last_code,'302');db.close();
 }finally{await s.cleanup();await new Promise<void>(r=>receiver.close(()=>r()));}
});

// Real WS protocol probes; never executes an external effect.
async function rawLink(s:any,credentials=s.device){
 const {default:WebSocket}=await import('ws');
 const response=await s.api({authorization:'Bearer '+credentials.deviceSecret},'POST','/api/v1/device-sessions',{deviceId:credentials.deviceId,keyId:credentials.keyId});assert.equal(response.status,201);
 const address=s.app.server.address();const ws=new WebSocket('ws://127.0.0.1:'+address.port+'/api/v1/device-sessions',{headers:{authorization:'Bearer '+response.body.accessToken}});const frames:any[]=[];let hello:any;let seq=0;let closed=false;
 ws.on('message',(raw:any)=>{const j=JSON.parse(String(raw));if(j.kind==='hello_ack')hello=j;else frames.push(j);});ws.on('close',()=>{closed=true;});
 await new Promise<void>((resolve,reject)=>{ws.once('open',()=>{ws.send(JSON.stringify({kind:'hello',supportedVersions:['1.0'],mode:'synthetic',capabilities:['message.send_text']}));resolve();});ws.once('error',reject);});await until(()=>hello,Boolean);
 const send=(kind:string,body:any)=>{const eventId='evt_'+randomBytes(12).toString('hex');ws.send(JSON.stringify({version:'1.0',kind,eventId,tenantId:hello.tenantId,deviceId:hello.deviceId,sessionId:hello.sessionId,seq:++seq,sentAt:new Date().toISOString().replace(/\.\d{3}Z$/,'Z'),mode:'synthetic',body}));return eventId;};
 const close=async()=>{if(!closed){ws.close();await until(()=>closed,Boolean);}};
 return {frames,send,close,isClosed:()=>closed};
}
test('lost unstarted rejected/cancelled/expired receipts reconcile after real WS reconnect; immutable duplicate ACK',async()=>{
 for(const status of ['rejected','cancelled','expired']){const s=await setup();let link:any;try{
 link=await rawLink(s);const m=await s.message();const frame=await until(()=>link.frames.find((x:any)=>x.kind==='command'),Boolean);const b=frame.body;
 link.send('accepted',{commandId:b.commandId,attemptId:b.attemptId});await until(()=>s.api(s.admin,'GET','/api/v1/commands/'+b.commandId),x=>x.body.status==='accepted');await link.close();await until(()=>s.api(s.admin,'GET','/api/v1/commands/'+b.commandId),x=>x.body.status==='execution_unknown');
 link=await rawLink(s);const result={commandId:b.commandId,attemptId:b.attemptId,status,effectState:'not_started',code:'SYNTHETIC_JOURNAL_RECOVERY',evidenceRefs:['journal_'+b.commandId]};const first=link.send('execution_result',result);await until(()=>link.frames.find((x:any)=>x.kind==='result_ack'&&x.body.ackEventId===first),Boolean);assert.equal((await s.api(s.headers,'GET',prefix+'/messages/'+m.body.messageId)).body.children[0].status,status);
 const second=link.send('execution_result',result);await until(()=>link.frames.find((x:any)=>x.kind==='result_ack'&&x.body.ackEventId===second),Boolean);
 link.send('execution_result',{...result,code:'CONFLICT_CHANGED'});await until(()=>link.isClosed(),Boolean);assert.equal((await s.api(s.headers,'GET',prefix+'/messages/'+m.body.messageId)).body.children[0].result.code,result.code);
 const records=(await s.api(s.admin,'GET','/api/v1/audit')).body.items.filter((x:any)=>x.action==='command.result_conflict');assert.equal(records.length,1);assert.equal(records[0].targetId,b.commandId);assert.equal(records[0].outcome,'ILLEGAL_TRANSITION');assert.ok(!JSON.stringify(records).includes('CONFLICT_CHANGED'));assert.ok(!JSON.stringify(records).includes('synthetic text'));assert.ok(!JSON.stringify(records).includes(s.a.token));
 }finally{await link?.close();await s.cleanup();}}
});
test('reconciliation rejects wrong attempt, empty refs, wrong device and prior running history',async()=>{
 for(const variant of ['attempt','empty','device','running']){const s=await setup();let link:any;try{
 link=await rawLink(s);await s.message();const frame=await until(()=>link.frames.find((x:any)=>x.kind==='command'),Boolean);const b=frame.body;link.send('accepted',{commandId:b.commandId,attemptId:b.attemptId});await until(()=>s.api(s.admin,'GET','/api/v1/commands/'+b.commandId),x=>x.body.status==='accepted');
 if(variant==='running'){link.send('started',{commandId:b.commandId,attemptId:b.attemptId});await until(()=>s.api(s.admin,'GET','/api/v1/commands/'+b.commandId),x=>x.body.status==='running');}await link.close();await until(()=>s.api(s.admin,'GET','/api/v1/commands/'+b.commandId),x=>x.body.status==='execution_unknown');
 let credentials=s.device;if(variant==='device'){const e=await s.api(s.admin,'POST','/api/v1/enrollment-sessions',{deviceLabel:'other synthetic',capabilities:['message.send_text'],expiresInSeconds:300});credentials=(await s.api({},'POST','/api/v1/devices/enroll',{enrollmentId:e.body.enrollmentId,enrollmentToken:e.body.enrollmentToken,deviceLabel:'other synthetic',localConfirmed:true})).body;}
 link=await rawLink(s,credentials);link.send('execution_result',{commandId:b.commandId,attemptId:variant==='attempt'?'attempt_wrong':b.attemptId,status:'cancelled',effectState:'not_started',code:'SYNTHETIC_RECOVERY',evidenceRefs:variant==='empty'?[]:['journal_'+b.commandId]});await until(()=>link.isClosed(),Boolean);const stored=(await s.api(s.admin,'GET','/api/v1/commands/'+b.commandId)).body;assert.equal(stored.status,'execution_unknown');assert.equal(stored.result,null);assert.equal((await s.api(s.admin,'GET','/api/v1/audit')).body.items.filter((x:any)=>x.action==='command.result_conflict').length,0);
 }finally{await link?.close();await s.cleanup();}}
});
test('token rotation/expiry invalidate queued app command authorization; callback newline rejected',async()=>{
 for(const variant of ['rotate','expiry']){const s=await setup();try{const m=await s.message();assert.equal(m.status,202);if(variant==='rotate')assert.equal((await s.api(s.admin,'POST',prefix+'/applications/'+s.a.appId+'/rotate',{})).status,200);else{const db=new DatabaseSync(join(s.dir,'server.sqlite'));db.prepare('UPDATE developer_apps SET token_expires=0 WHERE id=?').run(s.a.appId);db.close();}const dev=await s.connect();await until(()=>s.api(s.admin,'GET','/api/v1/commands/'+m.body.children[0].commandId),x=>x.body.status==='cancelled');assert.equal(dev.inbox().length,0);}finally{await s.cleanup();}}
 const {signature}=await import('../server/developer/callbacks.ts');const key=randomBytes(32),timestamp=String(Date.now()),body='{}',event='callback_test';const sig=signature(key,event,timestamp,body);assert.equal(verifyCallback(key,event,timestamp,body,sig+'\n'),false);assert.equal(verifyCallback(key,event,timestamp+'\n',body,signature(key,event,timestamp+'\n',body)),false);
});
test('callback total deadline bounds slow incomplete headers and oversized bodies fail closed',async()=>{
 const {createServer:netServer}=await import('node:net');const {deliver}=await import('../server/developer/callbacks.ts');
 let sockets=0;const receiver=netServer(socket=>{sockets++;socket.on('error',()=>{});socket.write('HTTP/1.1 200 OK\r\nX-Slow: ');const timer=setInterval(()=>socket.write('x'),100);socket.on('close',()=>clearInterval(timer));});await new Promise<void>(r=>receiver.listen(0,'127.0.0.1',r));const url=new URL('http://127.0.0.1:'+(receiver.address() as any).port+'/slow');
 try{const started=Date.now();assert.equal(await deliver(url,'{}',{}),0);assert.ok(Date.now()-started<3000);const count=sockets;assert.equal(await deliver(url,'x'.repeat(65537),{}),0);assert.equal(sockets,count);}finally{await new Promise<void>(r=>receiver.close(()=>r()));}
});
