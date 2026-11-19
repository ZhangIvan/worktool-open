import { DatabaseSync } from 'node:sqlite';
import { createHash, randomUUID } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { EventEmitter } from 'node:events';
import WebSocket from 'ws';
import Ajv2020 from 'ajv/dist/2020.js';
import addFormats from 'ajv-formats';
import canonicalize from 'canonicalize';

const id = () => 'evt_' + randomUUID().replaceAll('-', '');
const hash = (x: unknown) => createHash('sha256').update(canonicalize(x)!).digest('hex');
const ajv = new Ajv2020({ strict: false }); addFormats(ajv);
const schema = (name: string) => JSON.parse(readFileSync(new URL('../contracts/'+name, import.meta.url), 'utf8'));
type Wire = {version:string; kind:string; eventId:string; tenantId:string; deviceId:string; sessionId:string; seq:number; sentAt:string; mode:string; body:any};
type Hello = {kind:string;version:string;tenantId:string;deviceId:string;sessionId:string;policyVersion:number;serverTime:string;heartbeatIntervalSec:number;maxFrameBytes:number;mode:string};
const validateWire = ajv.compile<Wire>(schema('wire.schema.json'));
const validateHello = ajv.compile<Hello>(schema('hello-ack.schema.json'));
export type DeviceConfig = {
 baseUrl: string; deviceId: string; keyId: string; deviceSecret: string; dbPath: string;
 clock?: () => number;
 exports?: Record<string, { approvalId: string; scopeDigest: string; localConfirmed: boolean; syntheticContent: string }>;
 confirm?: (body: any) => boolean | Promise<boolean>;
 beforeExecute?: (body: any) => Promise<void>;
 fault?: 'none' | 'drop_result' | 'unknown_after_effect';
};
/** Synthetic only. There is deliberately no filesystem export reader or Android driver. */
export class SyntheticDevice extends EventEmitter {
 config: DeviceConfig; db: DatabaseSync; ws?: WebSocket; hello: any; outSeq=0; inSeq=0;
 revoked=false; stopped=false; heartbeat?: ReturnType<typeof setInterval>; chain=Promise.resolve();
 constructor(config: DeviceConfig) {
  super(); this.config=config;
  const url=new URL(config.baseUrl);
  if(!['http:','https:'].includes(url.protocol)||url.username||url.password||url.search||url.hash||!['127.0.0.1','localhost','[::1]'].includes(url.hostname)) throw Error('SIMULATOR_LOOPBACK_ONLY');
  this.db=new DatabaseSync(config.dbPath);
  this.db.exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL;
 CREATE TABLE IF NOT EXISTS commands(command_id TEXT PRIMARY KEY,digest TEXT NOT NULL,attempt_id TEXT NOT NULL,stage TEXT NOT NULL,result TEXT,acked INTEGER DEFAULT 0,stop INTEGER DEFAULT 0);
 CREATE TABLE IF NOT EXISTS inbox(command_id TEXT PRIMARY KEY,target_id TEXT NOT NULL,text TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS exports(command_id TEXT PRIMARY KEY,export_id TEXT NOT NULL,content_hash TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS stops(command_id TEXT,attempt_id TEXT,PRIMARY KEY(command_id,attempt_id));
 CREATE TABLE IF NOT EXISTS events(session_id TEXT,event_id TEXT,digest TEXT,PRIMARY KEY(session_id,event_id));`);
  // Interrupted started operations cannot be replayed even if no synthetic artifact is visible.
  const interrupted=this.db.prepare("SELECT * FROM commands WHERE stage='running' AND result IS NULL").all() as any[];
  for(const row of interrupted) this.persistResult(row.command_id,row.attempt_id,'execution_unknown','unknown','RECOVERED_INTERRUPTED');
 }
 now(){return (this.config.clock??Date.now)();}
 stamp(){return new Date(this.now()).toISOString().replace(/\.\d{3}Z$/,'Z');}
 async connect(){
  const res=await fetch(this.config.baseUrl+'/api/v1/device-sessions',{method:'POST',redirect:'error',headers:{'content-type':'application/json',authorization:'Bearer '+this.config.deviceSecret},body:JSON.stringify({deviceId:this.config.deviceId,keyId:this.config.keyId})});
  if(!res.ok) throw Error('SESSION_REJECTED_'+res.status);
  const session:any=await res.json(); this.outSeq=0;this.inSeq=0;this.hello=undefined;
  const wsUrl=this.config.baseUrl.replace(/^http/,'ws')+'/api/v1/device-sessions';
  this.ws=new WebSocket(wsUrl,{headers:{authorization:'Bearer '+session.accessToken},maxPayload:65536});
  await new Promise<void>((resolve,reject)=>{
   const timer=setTimeout(()=>reject(Error('HELLO_TIMEOUT')),5000);
   this.ws!.on('open',()=>this.ws!.send(JSON.stringify({kind:'hello',supportedVersions:['1.0'],mode:'synthetic',capabilities:['message.send_text','file.export']})));
   this.ws!.on('message',data=>{
    let msg:any;try{msg=JSON.parse(data.toString());}catch{this.fail('INVALID_JSON');return;}
    if(!this.hello){
     if(!validateHello(msg)||msg.deviceId!==this.config.deviceId||Math.abs(Date.parse(msg.serverTime)-this.now())>30000){clearTimeout(timer);reject(Error('HELLO_INVALID'));this.fail('HELLO_INVALID');return;}
     this.hello=msg;clearTimeout(timer);resolve();
     this.heartbeat=setInterval(()=>this.send('heartbeat',{policyVersion:this.hello.policyVersion,lastReceivedSeq:this.inSeq}),15000);this.heartbeat.unref();
     this.retransmit();return;
    }
    // Control messages are processed immediately so stop requests can interrupt a waiting executor.
    this.receive(msg);
   });
   this.ws!.on('error',()=>{clearTimeout(timer);reject(Error('SOCKET_ERROR'));});
   this.ws!.on('close',()=>{if(this.heartbeat)clearInterval(this.heartbeat);this.emit('disconnected');});
  });
 }
 fail(code:string){this.emit('protocolError',code);this.ws?.close(1002,code);}
 send(kind:string,body:any){
  if(!this.hello||this.ws?.readyState!==WebSocket.OPEN)return;
  const msg={version:'1.0',kind,eventId:id(),tenantId:this.hello.tenantId,deviceId:this.hello.deviceId,sessionId:this.hello.sessionId,seq:++this.outSeq,sentAt:this.stamp(),mode:'synthetic',body};
  if(!validateWire(msg)){this.fail('OUTGOING_SCHEMA');return;}
  this.ws.send(JSON.stringify(msg));
 }
 receive(msg:any){
  if(!validateWire(msg)||!['command','result_ack','stop_request','config_notification','transport_ack'].includes(msg.kind)||msg.tenantId!==this.hello.tenantId||msg.deviceId!==this.hello.deviceId||msg.sessionId!==this.hello.sessionId){this.fail('WIRE_REJECTED');return;}
  const digest=hash(msg);const seen:any=this.db.prepare('SELECT digest FROM events WHERE session_id=? AND event_id=?').get(msg.sessionId,msg.eventId);
  if(seen){if(seen.digest!==digest)this.fail('EVENT_CONFLICT');else if(msg.kind!=='transport_ack')this.send('transport_ack',{ackEventId:msg.eventId});return;}
  if(msg.seq!==this.inSeq+1){this.fail('SEQ_REJECTED');return;}
  this.db.prepare('INSERT INTO events VALUES(?,?,?)').run(msg.sessionId,msg.eventId,digest);this.inSeq=msg.seq;
  if(msg.kind==='result_ack'){this.db.prepare('UPDATE commands SET acked=1 WHERE command_id=?').run(msg.body.commandId);return;}
  if(msg.kind==='stop_request'){
   this.db.prepare('INSERT OR IGNORE INTO stops VALUES(?,?)').run(msg.body.commandId,msg.body.attemptId);
   this.db.prepare('UPDATE commands SET stop=1 WHERE command_id=? AND attempt_id=?').run(msg.body.commandId,msg.body.attemptId);
   if(msg.body.reason==='policy_revoked')this.revoked=true;
   this.send('transport_ack',{ackEventId:msg.eventId});return;
  }
  if(msg.kind==='config_notification'){this.hello.policyVersion=msg.body.policyVersion;this.revoked=msg.body.revoked;this.send('transport_ack',{ackEventId:msg.eventId});return;}
  if(msg.kind==='command')this.chain=this.chain.then(()=>this.execute(msg.body)).catch(()=>this.fail('EXECUTOR_ERROR'));
 }
 persistResult(commandId:string,attemptId:string,status:string,effectState:string,code:string){
  const result={commandId,attemptId,status,effectState,code,evidenceRefs:['journal_'+commandId]};
  this.db.prepare('UPDATE commands SET stage=?,result=? WHERE command_id=?').run(status,JSON.stringify(result),commandId);return result;
 }
 result(b:any,status:string,effect:string,code:string){const r=this.persistResult(b.commandId,b.attemptId,status,effect,code);if(this.config.fault!=='drop_result')this.send('execution_result',r);this.emit('result',r);}
 async execute(b:any){
  const existing:any=this.db.prepare('SELECT * FROM commands WHERE command_id=?').get(b.commandId);
  if(existing){
   if(existing.digest!==b.payloadDigest||existing.attempt_id!==b.attemptId){this.fail('COMMAND_CONFLICT');return;}
   if(existing.result)this.send('execution_result',JSON.parse(existing.result));
   else {this.send('accepted',{commandId:b.commandId,attemptId:b.attemptId});this.result(b,'execution_unknown','unknown','DUPLICATE_PENDING_NO_REPLAY');}return;
  }
  const expected=hash({capability:b.capability,payload:b.payload,expiresAt:b.expiresAt,policyVersion:b.policyVersion,requiresLocalConfirmation:b.requiresLocalConfirmation,deviceId:this.config.deviceId});
  if(expected!==b.payloadDigest){this.fail('DIGEST_MISMATCH');return;}
  this.db.prepare('INSERT INTO commands(command_id,digest,attempt_id,stage) VALUES(?,?,?,?)').run(b.commandId,b.payloadDigest,b.attemptId,'accepted');
  this.send('accepted',{commandId:b.commandId,attemptId:b.attemptId});
  const guard=()=>{
   const local:any=this.db.prepare('SELECT stop FROM commands WHERE command_id=?').get(b.commandId);
   if(local.stop||this.stopped||this.db.prepare('SELECT 1 FROM stops WHERE command_id=? AND attempt_id=?').get(b.commandId,b.attemptId))return 'STOP_REQUESTED';
   if(this.revoked||b.policyVersion!==this.hello.policyVersion)return 'POLICY_REVOKED';
   if(this.ws?.readyState!==WebSocket.OPEN)return 'OFFLINE';
   if(Date.parse(b.expiresAt)<=this.now())return 'EXPIRED';
   if(Date.parse(b.expiresAt)-this.now()>330000)return 'TTL_INVALID';return '';
  };
  let reason=guard();if(reason){this.result(b,reason==='STOP_REQUESTED'?'cancelled':'rejected','not_started',reason);return;}
  if(b.requiresLocalConfirmation && !(await this.config.confirm?.(b))){this.result(b,'rejected','not_started','LOCAL_CONFIRMATION_REQUIRED');return;}
  if(b.capability==='file.export'){
   const e=this.config.exports?.[b.payload.exportId];
   if(!e||!e.localConfirmed||e.approvalId!==b.payload.approvalId||e.scopeDigest!==b.payload.scopeDigest){this.result(b,'rejected','not_started','EXPORT_APPROVAL_MISMATCH');return;}
  }
  await this.config.beforeExecute?.(b);reason=guard();
  if(reason){this.result(b,reason==='STOP_REQUESTED'?'cancelled':'rejected','not_started',reason);return;}
  this.db.prepare("UPDATE commands SET stage='running' WHERE command_id=?").run(b.commandId);
  this.send('started',{commandId:b.commandId,attemptId:b.attemptId});
  if(b.capability==='message.send_text')this.db.prepare('INSERT INTO inbox VALUES(?,?,?)').run(b.commandId,b.payload.targetId,b.payload.text);
  else if(b.capability==='file.export'){const e=this.config.exports![b.payload.exportId];this.db.prepare('INSERT INTO exports VALUES(?,?,?)').run(b.commandId,b.payload.exportId,hash(e.syntheticContent));}
  else {this.result(b,'failed','not_applied','CAPABILITY_UNAVAILABLE');return;}
  if(this.config.fault==='unknown_after_effect')this.result(b,'execution_unknown','unknown','SYNTHETIC_FAULT_AFTER_EFFECT');
  else this.result(b,'succeeded','applied','SYNTHETIC_APPLIED');
 }
 retransmit(){for(const r of this.db.prepare('SELECT result FROM commands WHERE result IS NOT NULL AND acked=0').all() as any[])this.send('execution_result',JSON.parse(r.result));}
 emergencyStop(){this.stopped=true;this.db.prepare('UPDATE commands SET stop=1 WHERE result IS NULL').run();}
 inbox(){return this.db.prepare('SELECT * FROM inbox').all();}
 journal(){return this.db.prepare('SELECT command_id,stage,acked,stop FROM commands').all();}
 async close(){if(this.heartbeat)clearInterval(this.heartbeat);if(this.ws&&this.ws.readyState!==WebSocket.CLOSED){await new Promise<void>(resolve=>{this.ws!.once('close',()=>resolve());this.ws!.close();setTimeout(()=>{this.ws?.terminate();resolve();},500).unref();});}await this.chain;this.db.close();}
}
