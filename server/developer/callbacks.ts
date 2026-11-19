import {createHmac,timingSafeEqual} from 'node:crypto';
import {request} from 'node:http';
export function signature(key:Buffer,event:string,timestamp:string,body:string){return createHmac('sha256',key).update(event+'.'+timestamp+'.'+body).digest('hex');}
export function verifyCallback(key:Buffer,event:string,timestamp:string,body:string,sig:string,now=Date.now()){
 if(!/^\d{10,13}(?![\s\S])/.test(timestamp)||Math.abs(now-Number(timestamp))>30000||!/^[a-f0-9]{64}(?![\s\S])/.test(sig))return false;
 return timingSafeEqual(Buffer.from(signature(key,event,timestamp,body),'hex'),Buffer.from(sig,'hex'));
}
export function receiverURL(value:string,origin?:string){
 if(!origin)throw Error('CALLBACK_NETWORK_DISABLED');
 const o=new URL(origin),u=new URL(value);
 if(o.protocol!=='http:'||o.hostname!=='127.0.0.1'||!o.port||o.username||o.password||o.pathname!=='/'||o.search||o.hash||u.origin!==o.origin||u.username||u.password||u.hash)throw Error('CALLBACK_DESTINATION_DENIED');
 return u;
}
export async function deliver(url:URL,body:string,headers:Record<string,string>):Promise<number>{
 if(Buffer.byteLength(body)>65536)return 0;
 return new Promise(resolve=>{
  let settled=false;
  const finish=(code:number)=>{if(settled)return;settled=true;clearTimeout(deadline);resolve(code);};
  const req=request(url,{method:'POST',headers:{...headers,'content-type':'application/json','content-length':String(Buffer.byteLength(body))},timeout:2000},res=>{
   // Only status is meaningful; never drain an unbounded response body.
   finish(res.statusCode??0);res.destroy();req.destroy();
  });
  const deadline=setTimeout(()=>{finish(0);req.destroy();},2000);
  req.on('timeout',()=>{finish(0);req.destroy();});req.on('error',()=>finish(0));req.end(body);
 });
}
