import { useCallback, useEffect, useRef, useState } from 'react';
import { api, errorText, newKey, type RequestOptions } from './api';
export function useRead<T>(path: string | null, interval = 0) {
  const [data,setData] = useState<T|null>(null); const [error,setError] = useState(''); const [loading,setLoading] = useState(false); const [revision,setRevision] = useState(0); const [updatedAt,setUpdatedAt] = useState<Date|null>(null);
  const refresh = useCallback(()=>setRevision(v=>v+1),[]);
  useEffect(()=>{ setData(null); setUpdatedAt(null); },[path]);
  useEffect(()=>{
    if (!path) {setLoading(false);return;}
    const abort = new AbortController(); setLoading(true);setError('');
    api<T>(path,{signal:abort.signal}).then(value=>{if(!abort.signal.aborted){setData(value);setUpdatedAt(new Date());}}).catch(e=>{if(!abort.signal.aborted){setError(errorText(e));setData(null);}}).finally(()=>{if(!abort.signal.aborted)setLoading(false);});
    return ()=>abort.abort();
  },[path,revision]);
  useEffect(()=>{if(!interval||!path)return; const id=setInterval(refresh,interval);return()=>clearInterval(id);},[path,interval,refresh]);
  return {data,error,loading,refresh,updatedAt};
}
export function useWrite() {
  const [pending,setPending] = useState(false); const [error,setError] = useState(''); const locked = useRef(false); const keys=useRef(new Map<string,string>());
  async function run<T>(path:string, body:unknown, key?:string): Promise<T|undefined> {
    if(locked.current)return; locked.current=true;setPending(true);setError('');
    const isAuth=path.startsWith('/api/v1/auth/');
    const identity=isAuth?path:path+JSON.stringify(body);
    if(!isAuth&&!keys.current.has(identity))keys.current.set(identity,newKey());
    try {return await api<T>(path,{method:'POST',body,...(!isAuth?{key:key??keys.current.get(identity)}:{})} as RequestOptions);}
    catch(e){setError(errorText(e));throw e;}
    finally{locked.current=false;setPending(false);}
  }
  return {run,pending,error,clearError:()=>setError('')};
}
export function useRoute() {
  const read=()=>window.location.hash.slice(1)||'/overview'; const [route,setRoute]=useState(read);
  useEffect(()=>{const fn=()=>setRoute(read()); window.addEventListener('hashchange',fn);return()=>window.removeEventListener('hashchange',fn);},[]);
  return route;
}
/** Keep unsent work explicit across in-app links, Back and full-page unload. */
export function useNavigationGuard(enabled:boolean, message:string) {
  const enabledRef=useRef(enabled);enabledRef.current=enabled;
  const messageRef=useRef(message);messageRef.current=message;
  const allow=useRef(false);
  useEffect(()=>{
    let previous=window.location.hash;let restoring=false;
    const before=(e:BeforeUnloadEvent)=>{if(enabledRef.current&&!allow.current){e.preventDefault();e.returnValue='';}};
    const click=(e:MouseEvent)=>{
      if(e.defaultPrevented||e.button!==0||e.metaKey||e.ctrlKey||e.shiftKey||e.altKey)return;
      const anchor=(e.target as Element|null)?.closest('a[href^="#/"]') as HTMLAnchorElement|null;
      if(!anchor||anchor.hash===window.location.hash||!enabledRef.current)return;
      if(!window.confirm(messageRef.current)){e.preventDefault();e.stopImmediatePropagation();}else allow.current=true;
    };
    const hash=()=>{
      if(restoring){restoring=false;return;}
      const next=window.location.hash;
      if(next===previous)return;
      if(allow.current){allow.current=false;previous=next;return;}
      if(enabledRef.current&&!window.confirm(messageRef.current)){
        restoring=true;history.pushState(null,'',previous||'#/overview');window.dispatchEvent(new HashChangeEvent('hashchange'));
      }else previous=next;
    };
    window.addEventListener('beforeunload',before);document.addEventListener('click',click,true);window.addEventListener('hashchange',hash);
    return()=>{window.removeEventListener('beforeunload',before);document.removeEventListener('click',click,true);window.removeEventListener('hashchange',hash);};
  },[]);
  return ()=>{allow.current=true;};
}
