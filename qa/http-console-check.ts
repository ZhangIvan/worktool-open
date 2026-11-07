import { createApp } from '../server/app.ts';
import { spawn } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { resolve, join } from 'node:path';
import assert from 'node:assert/strict';
mkdirSync('.runtime',{recursive:true,mode:0o700});const dir=mkdtempSync(resolve('.runtime/http_'));
const password=randomBytes(24).toString('base64url');const origin='http://127.0.0.1:5173';
const app=await createApp({dbPath:join(dir,'server.sqlite'),bootstrap:{tenantId:'tenant_demo',users:[{principalId:'operator_demo',loginName:'operator',password,role:'operator'}]},allowedOrigins:[origin]});
await app.listen({host:'127.0.0.1',port:3100});
const child=spawn(process.execPath,['node_modules/vite/bin/vite.js','--config','console/vite.config.ts','--host','127.0.0.1'],{stdio:'ignore'});
const checks:any[]=[];
try {
 let html='';for(let i=0;i<100;i++){try{const r=await fetch(origin);if(r.ok){html=await r.text();break;}}catch{}await new Promise(r=>setTimeout(r,50));}
 assert.match(html,/src\/main.tsx/);checks.push({name:'Vite serves real console index',pass:true});
 const login=await fetch(origin+'/api/v1/auth/login',{method:'POST',headers:{origin,'content-type':'application/json'},body:JSON.stringify({tenantId:'tenant_demo',loginName:'operator',password})});assert.equal(login.status,200);const cookie=login.headers.get('set-cookie')!.split(';')[0];const b:any=await login.json();checks.push({name:'same-origin proxy real authentication',pass:true});
 const me=await fetch(origin+'/api/v1/auth/me',{headers:{cookie}});assert.equal(me.status,200);assert.equal((await me.json() as any).user.role,'operator');checks.push({name:'proxied cookie session user',pass:true});
 const list=await fetch(origin+'/api/v1/devices',{headers:{cookie}});assert.equal(list.status,200);assert.deepEqual((await list.json() as any).items,[]);checks.push({name:'proxied empty device list',pass:true});
 const logout=await fetch(origin+'/api/v1/auth/logout',{method:'POST',headers:{origin,cookie,'content-type':'application/json','x-csrf-token':b.csrfToken},body:'{}'});assert.equal(logout.status,204);assert.equal((await fetch(origin+'/api/v1/auth/me',{headers:{cookie}})).status,401);checks.push({name:'proxied CSRF logout invalidates session',pass:true});
 writeFileSync('artifacts/http-console-results.json',JSON.stringify({checks,browser:'BLOCKED_NOT_RUN',scope:'HTTP serving/proxy integration, not rendered UI interaction'},null,2));console.log(checks.length+' HTTP console checks passed');
} finally {child.kill('SIGTERM');await new Promise(r=>child.once('exit',r));await app.close();rmSync(dir,{recursive:true,force:true});}
