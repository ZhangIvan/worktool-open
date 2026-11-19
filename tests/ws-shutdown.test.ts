import {test} from 'node:test';
import assert from 'node:assert/strict';
import {randomBytes} from 'node:crypto';
import {mkdtempSync,rmSync} from 'node:fs';
import {fileURLToPath} from 'node:url';
import {join} from 'node:path';
import {DatabaseSync} from 'node:sqlite';
import WebSocket from 'ws';
import {createApp} from '../server/app.ts';
async function until(fn:()=>any,predicate:(v:any)=>boolean){for(let i=0;i<200;i++){const value=await fn();if(predicate(value))return value;await new Promise(r=>setTimeout(r,10));}assert.fail('condition timeout');}
for(const stage of ['delivered','accepted','running','replaced'])test('server close drains active WS '+stage+' transition before SQLite close and immediate restart',async()=>{
 const dir=mkdtempSync(fileURLToPath(new URL('./ws-shutdown-runtime_',import.meta.url))),dbPath=join(dir,'server.sqlite');const password=randomBytes(24).toString('hex'),origin='http://127.0.0.1:18888';
 const bootstrap={tenantId:'tenant_close',users:[{principalId:'admin_close',loginName:'admin',role:'admin' as const,password},{principalId:'operator_close',loginName:'operator',role:'operator' as const,password}]};
 let app=await createApp({dbPath,bootstrap,origin});let ws:WebSocket|undefined;let replaced:WebSocket|undefined;
 try{
 const base=await app.listen({host:'127.0.0.1',port:0});
 const auth=async(loginName:string)=>{const r=await app.inject({method:'POST',url:'/api/v1/auth/login',headers:{origin},payload:{tenantId:'tenant_close',loginName,password}});assert.equal(r.statusCode,200);return {origin,cookie:String(r.headers['set-cookie']).split(';')[0],'x-csrf-token':r.json().csrfToken};};
 const admin=await auth('admin'),operator=await auth('operator');
 const enrollment=await app.inject({method:'POST',url:'/api/v1/enrollment-sessions',headers:admin,payload:{deviceLabel:'Shutdown regression',capabilities:['message.send_text'],expiresInSeconds:300}});assert.equal(enrollment.statusCode,201);const e=enrollment.json();
 const enrolled=await app.inject({method:'POST',url:'/api/v1/devices/enroll',payload:{enrollmentId:e.enrollmentId,enrollmentToken:e.enrollmentToken,deviceLabel:'Shutdown regression',localConfirmed:true}});assert.equal(enrolled.statusCode,201);const d=enrolled.json();
 const session=await app.inject({method:'POST',url:'/api/v1/device-sessions',headers:{authorization:'Bearer '+d.deviceSecret},payload:{deviceId:d.deviceId,keyId:d.keyId}});assert.equal(session.statusCode,201);
 ws=new WebSocket(base.replace('http:','ws:')+'/api/v1/device-sessions',{headers:{authorization:'Bearer '+session.json().accessToken}});const frames:any[]=[];let hello:any,closed=false,seq=0;ws.on('message',raw=>{const v=JSON.parse(String(raw));if(v.kind==='hello_ack')hello=v;else frames.push(v);});ws.on('close',()=>{closed=true;});await new Promise<void>((resolve,reject)=>{ws!.once('open',()=>{ws!.send(JSON.stringify({kind:'hello',supportedVersions:['1.0'],mode:'synthetic',capabilities:['message.send_text']}));resolve();});ws!.once('error',reject);});await until(()=>hello,Boolean);
 if(stage==='replaced'){
  replaced=ws;(replaced as any)._socket.pause();
  ws=new WebSocket(base.replace('http:','ws:')+'/api/v1/device-sessions',{headers:{authorization:'Bearer '+session.json().accessToken}});frames.length=0;hello=undefined;closed=false;
  ws.on('message',raw=>{const v=JSON.parse(String(raw));if(v.kind==='hello_ack')hello=v;else frames.push(v);});ws.on('close',()=>{closed=true;});
  await new Promise<void>((resolve,reject)=>{ws!.once('open',()=>{ws!.send(JSON.stringify({kind:'hello',supportedVersions:['1.0'],mode:'synthetic',capabilities:['message.send_text']}));resolve();});ws!.once('error',reject);});await until(()=>hello,Boolean);
 }
 const submitted=await app.inject({method:'POST',url:'/api/v1/devices/'+d.deviceId+'/commands',headers:{...operator,'idempotency-key':'shutdown_'+stage},payload:{deviceId:d.deviceId,capability:'message.send_text',payload:{targetId:'synthetic_demo',text:'Synthetic shutdown test'},expiresAt:new Date(Date.now()+120000).toISOString().replace(/\.\d{3}Z$/,'Z'),requiresLocalConfirmation:false}});assert.equal(submitted.statusCode,202);const command=await until(()=>frames.find(x=>x.kind==='command'),Boolean);
 const send=(kind:string)=>ws!.send(JSON.stringify({version:'1.0',kind,eventId:'evt_'+randomBytes(12).toString('hex'),tenantId:hello.tenantId,deviceId:d.deviceId,sessionId:hello.sessionId,seq:++seq,sentAt:new Date().toISOString().replace(/\.\d{3}Z$/,'Z'),mode:'synthetic',body:{commandId:command.body.commandId,attemptId:command.body.attemptId}}));
 if(stage==='accepted'||stage==='running')send('accepted');if(stage==='running')send('started');
 await until(async()=>{const r=await app.inject({method:'GET',url:'/api/v1/commands/'+command.body.commandId,headers:admin});return r.json().status;},x=>x===(stage==='replaced'?'delivered':stage));
 // Do not close the device first: server shutdown must finish its own asynchronous WS handlers.
 const closing=app.close();await Promise.race([closing,new Promise((_,reject)=>{const timer=setTimeout(()=>reject(Error('SHUTDOWN_DID_NOT_TERMINATE_REPLACED_SOCKET')),3000);timer.unref();})]);replaced?.terminate();await new Promise(r=>setTimeout(r,80));await until(()=>closed,Boolean);
 // Observe persistence BEFORE restart recovery can conceal a lost close transition.
 const persisted=new DatabaseSync(dbPath);const row=persisted.prepare('SELECT status,result FROM commands WHERE id=?').get(command.body.commandId) as any;assert.equal(row.status,'execution_unknown');assert.equal(row.result,null);const events=persisted.prepare("SELECT count(*) AS n FROM command_events WHERE command=? AND event='lost_execution_evidence'").get(command.body.commandId) as any;assert.equal(events.n,1);persisted.close();
 app=await createApp({dbPath,bootstrap,origin});await app.listen({host:'127.0.0.1',port:0});await new Promise(r=>setTimeout(r,30));const reopened=new DatabaseSync(dbPath);assert.equal((reopened.prepare('SELECT status FROM commands WHERE id=?').get(command.body.commandId) as any).status,'execution_unknown');assert.equal((reopened.prepare("SELECT count(*) AS n FROM command_events WHERE command=? AND event='lost_execution_evidence'").get(command.body.commandId) as any).n,1);reopened.close();
 }finally{replaced?.terminate();ws?.terminate();await app.close();rmSync(dir,{recursive:true,force:true});}
});
