/* Durable inspection destinations. Operation execution stays in existing explicit actions. */
(function(root){
 'use strict';
 const broker={summary:'details',vms:'vms',tasks:'tasks',events:'events',logs:'blacksmith-logs',certificates:'certificates',plans:'plans',endpoints:'broker',configs:'configs',settings:'config',manifest:'manifest'};
 const instance={summary:'details',vms:'vms',events:'events',logs:'instance-logs',certificates:'certificates',credentials:'credentials',testing:'testing',manifest:'manifest'};
 const labels={blacksmith:'Blacksmith Broker',services:'Service Instances',certificates:'Certificates',upgrade:'Batch Operations',details:'Summary',vms:'VMs',tasks:'Tasks',events:'Events','blacksmith-logs':'Logs','instance-logs':'Logs',plans:'Service plans',broker:'Endpoints',configs:'BOSH configs',config:'Broker Settings',manifest:'Manifest',credentials:'Credentials',testing:'Testing',upgrade:'Batch Operations'};
 const steps=['operation','options','instances','review'];
 const encode=encodeURIComponent;
 const key=(map,value)=>Object.keys(map).find(k=>map[k]===value);
 function format(r){
  let p;
  if(r.screen==='blacksmith') {p=['broker',key(broker,r.section||'details')||'summary'];if(r.section==='plans'&&r.service!=null&&r.plan!=null)p.push(r.service,r.plan);}
  else if(r.screen==='services')p=r.instance?['instances',r.instance,key(instance,r.section||'details')||'summary']:['instances'];
  else if(r.screen==='certificates')p=['certificates'];
  else if(r.screen==='upgrade')p=r.view==='activity'?['batch','activity',...(r.task?[r.task]:[])]:['batch','new',r.operation||'upgrade',steps[r.step||0]];
  else throw new Error('Unknown destination');
  let suffix='';if(r.screen==='services'&&!r.instance&&r.service)suffix='?service='+encode(r.service)+(r.plan?'&plan='+encode(r.plan):'');
  if(r.subview)suffix='?view='+encode(r.subview);if(r.endpoint)suffix+=(suffix?'&':'?')+'endpoint='+encode(r.endpoint);
  if(r.inspection){const params=new URLSearchParams(suffix.replace(/^\?/,''));params.set('inspect',r.inspection.kind);params.set('id',r.inspection.id);if(r.inspection.type)params.set('type',r.inspection.type);if(r.inspection.name)params.set('name',r.inspection.name);suffix='?'+params.toString();}
  return '#/'+p.map(encode).join('/')+suffix;
 }
 function parse(hash){
  try{
   const [raw,query='']=hash.replace(/^#\/?/,'').split('?');const p=raw.split('/').filter(Boolean).map(decodeURIComponent);const q=new URLSearchParams(query);let r;
   if(!p.length)r={screen:'blacksmith',section:'details'};
   else if(p[0]==='broker'&&p.length===1)r={screen:'blacksmith',section:'details'};
   else if(p[0]==='broker'&&broker[p[1]]&&(p.length===2||(p[1]==='plans'&&p.length===4))){r={screen:'blacksmith',section:broker[p[1]]};if(p.length===4){r.service=p[2];r.plan=p[3];}}
   else if(p[0]==='instances'&&(p.length<=3)&&(p.length!==3||instance[p[2]])){r={screen:'services'};if(p.length>=2){r.instance=p[1];r.section=instance[p[2]||'summary'];}}
   else if(p[0]==='certificates'&&p.length===1)r={screen:'certificates'};
   else if(p[0]==='batch'&&p[1]==='activity'&&p.length<=3){r={screen:'upgrade',view:'activity'};if(p[2])r.task=p[2];}
   else if(p[0]==='batch'&&p[1]==='new'&&['upgrade','rotate','converge','command'].includes(p[2])&&steps.includes(p[3])&&p.length===4)r={screen:'upgrade',view:'new',operation:p[2],step:steps.indexOf(p[3])};
   else if(p[0]==='batch'&&p.length===1||p[0]==='batch'&&p[1]==='new'&&p.length===2)r={screen:'upgrade',view:'new',operation:'upgrade',step:0};
   else if(p[0]==='batch'&&p[1]==='new'&&['upgrade','rotate','converge','command'].includes(p[2])&&p.length===3)r={screen:'upgrade',view:'new',operation:p[2],step:0};
   else return null;
   if(r.screen==='services'&&!r.instance&&q.get('service')){r.service=q.get('service');if(q.get('plan'))r.plan=q.get('plan');}
   if(q.has('view'))r.subview=q.get('view');if(q.has('endpoint')&&r.section==='broker')r.endpoint=q.get('endpoint');if(q.has('inspect')){if(!['task','config','certificate'].includes(q.get('inspect'))||!q.get('id'))return null;r.inspection={kind:q.get('inspect'),id:q.get('id')};if(r.inspection.kind==='task'&&!/^\d+$/.test(r.inspection.id))return null;if(r.inspection.kind==='config'){if(!q.get('type')||!q.get('name'))return null;r.inspection.type=q.get('type');r.inspection.name=q.get('name');}}return r;
  }catch(_){return null;}
 }
 function parent(r){if(r.inspection){const n={...r};delete n.inspection;return n;}if(r.plan)return {screen:r.screen,section:'plans'};if(r.subview){const n={...r};delete n.subview;return n;}if(r.instance&&r.section!=='details')return {screen:'services',instance:r.instance,section:'details'};if(r.instance)return {screen:'services'};if(r.screen==='blacksmith')return {screen:'blacksmith',section:'details'};if(r.screen==='upgrade'&&r.task)return {screen:'upgrade',view:'activity'};return {screen:'blacksmith',section:'details'};}
 function create(win,adapter){
  let current=null,currentEntryId=null,controller,sequence=0,started=false,applying=false,pendingHash=null;
  const session=String(Date.now())+Math.random();const entries=new Map();let entryId=0;
  function metadata(id,previous){return {blacksmithRoute:{session,id,previous}};}
  function remember(){
   // popstate has already changed history.state. Save under the entry we are
   // leaving, not the destination entry currently reported by the browser.
   if(currentEntryId===null)return;
   const previous=entries.get(currentEntryId)||{};
   entries.set(currentEntryId,{...previous,route:current,focus:win.document.activeElement,scroll:[...win.document.querySelectorAll('.tab-panel,.detail-content,.plan-detail,.plans-list,.services-items-container')].map(el=>({el,top:el.scrollTop,left:el.scrollLeft}))});
   while(entries.size>100)entries.delete(entries.keys().next().value);
  }
  function restore(id){const e=entries.get(id);if(!e)return;for(const s of e.scroll||[])if(s.el.isConnected){s.el.scrollTop=s.top;s.el.scrollLeft=s.left;}if(e.focus?.isConnected&&e.focus.getClientRects().length)e.focus.focus({preventScroll:true});}
  async function recover(error,route,options,signal,revision){
   const previous=current, previousEntry=currentEntryId;
   const fallback=previous||{screen:'blacksmith',section:'details'};
   try{
    const resolved=await adapter.activate(fallback,{signal,revision,history:true,recovery:true});
    if(signal.aborted||revision!==sequence)return false;
    if(!resolved)throw new Error('The previous view could not be restored.');
    current=resolved;
    const old=win.history.state?.blacksmithRoute;
    // A failed click has not changed the address. A failed history/direct
    // entry is replaced by the usable fallback; never forge a browser Back.
    if(win.location.hash!==format(resolved)||old?.session!==session){
     const id=++entryId;
     win.history.replaceState(metadata(id,null),'',format(resolved));
     currentEntryId=id;
    }else currentEntryId=old.id;
    adapter.changed?.(resolved,{history:true,recovered:true});
    restore(previousEntry);
   }catch(recoveryError){
    if(signal.aborted||revision!==sequence)return false;
    // Keep the requested URL if even the fallback cannot load. The alert
    // explains failure rather than announcing a destination as successful.
    current=null;currentEntryId=null;
   }
   adapter.error?.(error,route);
   return false;
  }
  async function apply(route,options={}){
   if(!route||options.signal?.aborted)return false;
   if(!applying&&current&&format(current)===format(route)&&!options.replace&&!options.cancelRestore){adapter.settled?.();return true;}
   pendingHash=format(route);if(!options.cancelRestore)remember();controller?.abort();controller=new win.AbortController();
   const requestController=controller,signal=controller.signal,revision=++sequence;applying=true;
   const cancel=()=>{
    requestController.abort();
    if(revision!==sequence)return;
    // An adapter may have already switched panels before its fetch completes.
    // Restore the source with a new revision and no new history entry.
    void apply(current||{screen:'blacksmith',section:'details'},{history:true,cancelRestore:true});
   };
   options.signal?.addEventListener('abort',cancel,{once:true});
   try{
    // The section is chosen now, so its name must not wait for its data.
    if(route.instance)adapter.pending?.(route);
    const resolved=await adapter.activate(route,{signal,revision,history:!!options.history});
    if(signal.aborted||revision!==sequence)return false;
    if(!resolved)throw new Error('This destination is not available.');
    const old=win.history.state?.blacksmithRoute;current=resolved;const hash=format(resolved);
    if(!options.history){
     if(options.replace||win.location.hash===hash){
      const id=old?.session===session?old.id:++entryId;
      win.history.replaceState(metadata(id,old?.session===session?old.previous:null),'',hash);
     }else{
      const id=++entryId;
      win.history.pushState(metadata(id,old?.session===session?old.id:null),'',hash);
     }
    }else if(win.location.hash!==hash||old?.session!==session){
     win.history.replaceState(old?.session===session?win.history.state:metadata(++entryId,null),'',hash);
    }
    currentEntryId=win.history.state?.blacksmithRoute?.id??null;
    adapter.changed?.(resolved,{history:!!options.history});
    if(options.history)restore(currentEntryId);
    return true;
   }catch(error){
    if(signal.aborted||revision!==sequence)return false;
    return await recover(error,route,options,signal,revision);
   }finally{options.signal?.removeEventListener('abort',cancel);if(revision===sequence){applying=false;pendingHash=null;}}
  }
  async function onHistory(){
   if(applying&&pendingHash===win.location.hash)return;
   const route=parse(win.location.hash);
   if(!route){
    const fallback=current||{screen:'blacksmith',section:'details'};
    await apply(fallback,{history:true,replace:true});
    adapter.error?.(new Error('This destination is not available.'),null);
    return;
   }
   if(current&&format(current)===format(route)&&!applying)return;
   await apply(route,{history:true});
  }
  const api={get current(){return current;},get applying(){return applying;},navigate:(r,o)=>apply(r,o),
   async start(){
    if(started)return;started=true;win.addEventListener('popstate',onHistory);win.addEventListener('hashchange',onHistory);
    const r=parse(win.location.hash);
    if(!r){await apply({screen:'blacksmith',section:'details'},{replace:true});adapter.error?.(new Error('This destination is not available.'),null);return false;}
    return apply(r,{replace:true});
   },
   record(route,options={}){
    if(applying||!started)return;const clean=parse(format(route));
    if(!clean)return;
    adapter.settled?.();
    if(current&&format(clean)===format(current))return;
    const old=win.history.state?.blacksmithRoute;
    // A replacing record rewrites the current entry, so refining a filter does not fill Back.
    if(options.replace&&old?.session===session){current=clean;win.history.replaceState(metadata(old.id,old.previous),'',format(clean));currentEntryId=old.id;adapter.changed?.(clean,{history:false,record:true});return;}
    remember();current=clean;
    const id=++entryId;win.history.pushState(metadata(id,old?.session===session?old.id:null),'',format(clean));currentEntryId=id;
    adapter.changed?.(clean,{history:false,record:true});
   },
   inspect(inspection){return apply({...current,inspection});},
   closeInspection(){
    if(!current?.inspection)return false;
    const route={...current};delete route.inspection;
    const meta=win.history.state?.blacksmithRoute;
    const source=meta?.session===session?entries.get(meta.previous)?.route:null;
    const sourceBase=source?{...source}:null;if(sourceBase)delete sourceBase.inspection;
    if(sourceBase&&format(sourceBase)===format(route)){win.history.back();return true;}
    // Direct entry or an unknown predecessor must stay inside the console.
    return apply(route,{replace:true});
   },
   async back(){const m=win.history.state?.blacksmithRoute;if(m?.session===session&&m.previous!=null){win.history.back();return;}return apply(parent(current||{screen:'blacksmith',section:'details'}));},
   destroy(){controller?.abort();win.removeEventListener('popstate',onHistory);win.removeEventListener('hashchange',onHistory);entries.clear();}
  };return api;
 }
 root.BlacksmithRoutes={parse,format,parent,create,broker,instance};
 if(!root.document)return;
 const doc=root.document;
 // Testing tabs whose label is not their id in sentence case. Tool names
 // keep their own spelling.
 const testingViews={test:'Connection',queues:'Queue info','queue-ops':'Queue ops',rabbitmqctl:'rabbitmqctl'};
 function render(route){
  let bar=doc.getElementById('route-context');if(!bar){bar=doc.createElement('div');bar.id='route-context';bar.className='route-context';doc.querySelector('.tab-content')?.before(bar);}
  if(!bar)return;bar.replaceChildren();const back=doc.createElement('button');back.type='button';back.className='route-back';back.textContent='← Back';back.addEventListener('click',()=>root.blacksmithRouter.back());bar.append(back);
  const nav=doc.createElement('nav');nav.setAttribute('aria-label','Breadcrumb');const list=doc.createElement('ol');nav.append(list);bar.append(nav);
  const crumbs=[];const push=(label,r)=>crumbs.push({label,r});
  push(route.screen==='upgrade'?'Batch Operations':labels[route.screen],route.screen==='blacksmith'?{screen:'blacksmith',section:'details'}:{screen:route.screen,...(route.screen==='upgrade'?{view:'new',operation:route.operation||'upgrade',step:0}:{})});
  if(route.instance)push(root.serviceInstances?.[route.instance]?.instance_name||route.instance,{screen:'services',instance:route.instance,section:'details'});
  if(route.section)push(labels[route.section]||route.section,{screen:route.screen,section:route.section,...(route.instance?{instance:route.instance}:{})});
  if(route.plan){const s=root.plansData?.services?.find(s=>s.id===route.service);push(s?.name||route.service,{screen:'blacksmith',section:'plans'});push(s?.plans?.find(p=>p.id===route.plan)?.name||route.plan,route);}
  if(route.screen==='upgrade'){if(route.view==='activity'){push('Activity',{screen:'upgrade',view:'activity'});if(route.task)push(root.blacksmithJobTitle?.(route.task)||'Job '+route.task,route);}else{push(({upgrade:'Upgrade',rotate:'Rotate certificates',converge:'Converge',command:'Run command'})[route.operation]||'New Operation',route);push(steps[route.step||0].replace(/^./,c=>c.toUpperCase()),route);}}
  if(route.inspection)push(({task:'BOSH task ',config:'Config version ',certificate:'Certificate '})[route.inspection.kind]+route.inspection.id,route);
  if(route.subview)push((route.section==='testing'&&testingViews[route.subview])||route.subview.replaceAll('-',' ').replace(/^./,c=>c.toUpperCase()),route);
  crumbs.forEach((c,i)=>{const li=doc.createElement('li');if(i===crumbs.length-1){const span=doc.createElement('span');span.textContent=c.label;span.setAttribute('aria-current','page');li.append(span);}else{const a=doc.createElement('a');a.textContent=c.label;a.href=format(c.r);a.addEventListener('click',e=>{if(e.ctrlKey||e.metaKey||e.shiftKey||e.altKey)return;e.preventDefault();root.blacksmithRouter.navigate(c.r);});li.append(a);}list.append(li);});
  doc.title=crumbs.map(c=>c.label).reverse().join(' · ')+' — Blacksmith';
  const heading=doc.getElementById('console-title');if(heading)heading.textContent=crumbs.map(c=>c.label).join(' / ');
  root.BlacksmithViewGuidance?.mount(bar,route);
 }
 root.blacksmithRefreshContext=()=>{const current=root.blacksmithRouter?.current;if(current&&current.view==='activity'&&current.task)render(current);};
 function error(error){let node=doc.getElementById('route-error');if(!node){node=doc.createElement('div');node.id='route-error';node.setAttribute('data-variant','danger');node.setAttribute('role','alert');doc.querySelector('.tab-content')?.before(node);}node.textContent=error.message+' Use Back or select a destination from the navigation.';}
 function start(){if(root.blacksmithRouter||!root.blacksmithViews)return;const router=create(root,{activate:(r,o)=>root.blacksmithViews.activate(r,o),settled:()=>doc.getElementById('route-error')?.remove(),pending:r=>render(r),changed:(r,o)=>{doc.getElementById('route-error')?.remove();render(r);root.blacksmithNav?.announce(doc.title.replace(' — Blacksmith',''));if(!r.inspection&&!o.history&&!o.record){const h=doc.querySelector('.tab-panel.active .detail-content')||doc.querySelector('.tab-panel.active');h?.setAttribute('tabindex','-1');h?.focus({preventScroll:true});}},error});root.blacksmithRouter=router;router.start();}
 doc.addEventListener('blacksmith:ready',start);if(root.blacksmithViews)start();
})(globalThis);
