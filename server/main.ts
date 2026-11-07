import {createApp,type Bootstrap} from './app.ts';
import {randomBytes} from 'node:crypto';
import {mkdirSync,existsSync,chmodSync} from 'node:fs';
import {dirname,resolve} from 'node:path';
const dbPath=resolve(process.env.WORKTOOL_DB??'server/data/prototype.sqlite');mkdirSync(dirname(dbPath),{recursive:true,mode:0o700});
let bootstrap:Bootstrap|undefined;
if(!existsSync(dbPath)){
 if(!process.stdout.isTTY)throw Error('First bootstrap requires a protected interactive terminal; use createApp test injection for automated synthetic tests.');
 bootstrap={tenantId:'tenant_local',users:['admin','operator','approver','auditor'].map(role=>({principalId:role+'_local',loginName:role,password:randomBytes(24).toString('base64url'),role:role as any}))};
 process.stdout.write('ONE-TIME LOCAL SYNTHETIC BOOTSTRAP. Do not paste into logs or source.\n'+JSON.stringify(bootstrap,null,2)+'\n');
}
const app=await createApp({dbPath,bootstrap,allowedOrigins:['http://127.0.0.1:3100','http://127.0.0.1:5173']});chmodSync(dbPath,0o600);
await app.listen({host:'127.0.0.1',port:3100});
for(const signal of ['SIGINT','SIGTERM'] as const)process.on(signal,()=>{app.close().then(()=>process.exit(0));});
