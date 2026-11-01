// One harness for every certificate UI suite. It loads ui/blacksmith.js and
// ui/index.html from this repository through relative paths and runs them in
// jsdom, with fetch replaced by a per-suite route table.
//
//   H.main      boots the full page and exposes internals through window.__T
//   H.instance  boots a minimal page for the per-instance certificates tab
//   H.fleet     boots the full page against a scripted certificate inventory
const fs = require('fs');
const path = require('path');
const { JSDOM } = require('./support.js');
const { install: installStubs } = require('../dom-stubs.js');
// Every harness window reports its uncaught errors through watchUncaught.
// tests/ui/uncaught-guard.js is preloaded by bunfig.toml, and its afterEach
// fails any test whose window raised one, so no suite has to opt in. The same
// text also lands in the env's errors list, which suites that assert on
// console output keep reading.
const { watchUncaught } = require('../watch-uncaught.js');

const UI = path.resolve(__dirname, '..', '..', '..', 'ui');
// icons.js loads before blacksmith.js in the page, so every boot evaluates it first.
const ICONS = fs.readFileSync(path.join(UI, 'icons.js'), 'utf8');

const main=(()=>{
const SRC=path.join(UI,'blacksmith.js');
const src=fs.readFileSync(SRC,'utf8');
const html=fs.readFileSync(path.join(UI,'index.html'),'utf8').replace(/<script[\s\S]*?<\/script>/g,'').replace(/<link[^>]*>/g,'');
const EVIL='"><img src=x onerror=alert(1)><script>alert(2)</script>\'';
const sleep=(ms)=>new Promise(r=>setTimeout(r,ms));
const day=86400000, now=Date.now(), iso=(ms)=>new Date(ms).toISOString();
const NAMES=['retainView','populateTaskFilter','formatEvents','statusBadge','configTypeBadge','formatStamp','stampHtml','certTimeText','certClockText','certDay','EMPTY','EMPTY_HTML','isEmptyValue','setCellText','formatManifestDetails','renderBlacksmithSettings','setupBlacksmithSettingsHandlers','refreshTasksTable','refreshConfigsTable','initializeSorting','tableOriginalData','initializeAllTables','renderPlansTemplate','renderPlanDetail','formatTasks','renderBrokerTab','initBrokerTab','formatConfigs','applyConfigsFilter','formatBlacksmithLogs','formatCredentials','formatInstanceLogs','mountLogNotice','formatJobLogContent','formatVMs','openAdvancedReleasePicker','syncAdvancedReleaseSummary','buildUpgradeFlowSummary','batchFlow','setBatchStep','showBatchReview','requestOperation','showConfirmDialog','showPromptDialog', 'CFRegistrationManager', 'renderServicesTemplate', 'renderBlacksmithTemplate','renderServiceDetail','renderBlacksmithOverview','setTheme','getInstanceStatusIcon','getInstanceStatusClass','taskDetailCache','decisionState','awaitingDecisionTaskIds','buildCertificateInstance','runDecision','holderDetails','upgradeTasks','buildDecisionPanel','buildRemediation','openRotateInstanceDialog','CertOverview','CertsFleet','readApiError','apiErrorText','renderUpgradeTaskList','renderTaskDetail','fetchUpgradeTasks','selectUpgradeTask','rerenderTaskPanel','renderPollStatus','initUpgradeTab','sendTaskControl',
 'renderOperation','renderRotateInstances','refreshUpgradeInstanceList','updateUpgradeButton','renderUpgradeInstances','setupUpgradeInstanceHandlers','loadRotateInventory','rotateEligibleChips','rotateState','selectedUpgradeInstances','computeUpgradeHolders','rotateSelectVisible','rotateClearVisible','rotateSelectMatching','renderRotateSelectionLine','rotateSelection','setUpgradeTasks','setServiceInstances','rotateReviewList',
 'buildModeControls','selectedUpgradeInstances','updateOperationFooter','batchPatchBlock','startUpgrade','upgradeOperation','previewAndCreate','rotateErrorText','buildPreviewBody','rotateFooterLabel','renderRotateTargets','normalizeSerial','displaySerial','sameSerial','hasUndeployedVersion','renderServiceTesting','detectServiceFamily','LogSelectionManager','renderRedisInfoOperation','batchOptionIssue','batchInstanceIssue','batchSelectedIds','batchMatchingRows','runBatchReview','recordCommandPreview','commandConfirmWord','recordCommandRefusal','commandState','buildCommandReviewBody','lockCommandControls','entityMetric'];
function boot(opts={}){
  const dom=new JSDOM(html,{runScripts:'outside-only',pretendToBeVisual:true,url:'http://localhost/'});
  const w=dom.window;
  const stubs=installStubs(w);
  w.Element.prototype.getClientRects=function(){return [1]};
  w.matchMedia=w.matchMedia||(()=>({matches:false,addListener(){},addEventListener(){}}));
  w.navigator.clipboard={writeText:()=>Promise.resolve()};
  w.CSS=w.CSS||{escape:(x)=>String(x).replace(/[^\w-]/g,'\\$&')};
  w.HTMLElement.prototype.scrollIntoView=function(){};
  const errors=[]; const calls=[]; const alerts=[];
  // A suite that passes noDialogs has every dialog call recorded and thrown, so
  // it fails on the first one and can assert alerts stays empty.
  const refuse=(kind)=>(m)=>{ alerts.push(kind+':'+m); if(opts.noDialogs) throw new Error('window.'+kind+' called'); return kind==='confirm'?true:(kind==='prompt'?'x':undefined); };
  w.alert=refuse('alert'); w.confirm=refuse('confirm'); w.prompt=refuse('prompt');
  // logs holds every console call, so a suite can prove a value never reached the console.
  // An error-like object shows its name, message, and stack beside its own fields,
  // because errors from another realm fail instanceof Error and stringify as {}.
  const flat=(x)=>{ try{ return JSON.stringify(x); }catch(e){ return String(x); } };
  const show=(x)=>{ if(!x||typeof x!=='object') return String(x); if(typeof x.message==='string') return [x.name,x.message,x.stack,flat(x)].join(' '); return flat(x); };
  const logs=[]; const keep=(...a)=>logs.push(a.map(show).join(' '));
  w.console.log=keep; w.console.warn=keep; w.console.info=keep; w.console.debug=keep; w.console.error=(...a)=>{errors.push(a.map(show).join(' ')); keep(...a);};
  watchUncaught(w,errors);
  w.console.trace=keep; w.console.dir=keep; w.console.table=keep; w.console.group=keep; w.console.groupCollapsed=keep;
  w.console.assert=(ok,...a)=>{ if(!ok) keep(...a); };
  w.__routes=(opts.routes||[]).slice();
  const json=(status,body)=>Promise.resolve({ok:status>=200&&status<300,status,statusText:'st',json:()=>Promise.resolve(body),text:()=>Promise.resolve(typeof body==='string'?body:JSON.stringify(body))});
  w.fetch=(u,o={})=>{
    u=String(u); calls.push((o.method||'GET')+' '+u+(o.body?' '+o.body:''));
    for(const [test,fn] of w.__routes){ if(typeof test==='string'?u.includes(test):test.test(u)){ const r=fn(u,o,json); if(r!==undefined) return r; } }
    return json(404,{success:false,error:'nf',code:'not_found'});
  };
  w.crypto=w.crypto||{}; 
  let uuidN=0; w.crypto.randomUUID=()=>'00000000-0000-4000-8000-'+String(++uuidN).padStart(12,'0');
  const idx=src.lastIndexOf('})(document, window)');
  const hookCode=NAMES.map(n=>`${n}: (typeof ${n}!=='undefined'?${n}:undefined)`).join(',');
  const hooked=src.slice(0,idx)+`;window.__T={${hookCode},
  _setTasks:(t)=>{ if(typeof upgradeTasks!=='undefined') upgradeTasks=t; },
  _setHolder:(id,detail)=>{ holderDetails.set(id,{detail,signature:'x',readAt:Date.now()}); },
  _setSelectedTask:(id)=>{ selectedTaskId=id; },
  _getSelectedTask:()=>selectedTaskId};\n`+src.slice(idx);
  try{ w.eval(ICONS); w.eval(hooked);}catch(e){ console.log('LOAD ERROR',e.stack.split('\n').slice(0,4).join('\n')); errors.push('LOAD ERROR '+e.message); }
  // guidance loads ui/view-guidance.js after blacksmith.js, in the page's order.
  if(opts.guidance) w.eval(fs.readFileSync(path.join(UI,'view-guidance.js'),'utf8'));
  // Oat loads after the page's own script, and defines the tabs element's base class.
  if(opts.oat) w.eval(fs.readFileSync(path.join(UI,'vendor','oat','oat.min.js'),'utf8'));
  return {w,d:w.document,errors,logs,calls,alerts,json,stubs};
}
return {boot,sleep,EVIL,day,now,iso};
})();

const instance=(()=>{
const SRC=path.join(UI,'blacksmith.js');
const src=fs.readFileSync(SRC,'utf8');
const EVIL='"><img src=x onerror=alert(1)><script>alert(2)</script>\'';
const sleep=(ms)=>new Promise(r=>setTimeout(r,ms));
const day=86400000, now=Date.now(), iso=(ms)=>new Date(ms).toISOString();

function boot(opts={}){
  const dom=new JSDOM('<!DOCTYPE html><html><body><button class="tab-button" data-tab="blacksmith"></button><div id="blacksmith" class="tab-panel"></div><div id="services"></div><button id="opener">o</button></body></html>',{runScripts:'outside-only',pretendToBeVisual:true,url:'http://localhost/'});
  const w=dom.window;
  const stubs=installStubs(w);
  w.Element.prototype.getClientRects=function(){return [1]};
  w.matchMedia=w.matchMedia||(()=>({matches:false,addListener(){},addEventListener(){}}));
  w.navigator.clipboard={writeText:(t)=>{w.__copied.push(t);return Promise.resolve();}};
  w.__copied=[];
  w.CSS=w.CSS||{escape:(x)=>String(x).replace(/[^\w-]/g,'\\$&')};
  w.HTMLElement.prototype.scrollIntoView=function(){};
  const errors=[]; const calls=[];
  watchUncaught(w,errors);
  w.console.log=()=>{}; w.console.error=(...a)=>errors.push(a.map(String).join(' ')); w.console.warn=()=>{};
  w.__routes=[];
  const json=(status,body)=>Promise.resolve({ok:status>=200&&status<300,status,statusText:'st',json:()=>Promise.resolve(body),text:()=>Promise.resolve(JSON.stringify(body))});
  w.__json=json;
  w.fetch=(u,o={})=>{
    u=String(u); calls.push((o.method||'GET')+' '+u+(o.body?' '+o.body:''));
    for(const [test,fn] of w.__routes){ if(typeof test==='string'?u.includes(test):test.test(u)){ return fn(u,o,json);} }
    return json(404,{success:false,error:'nf',code:'not_found'});
  };
  const idx=src.lastIndexOf('})(document, window)');
  const hooked=src.slice(0,idx)+';window.__T={renderServiceCertificatesTab,renderCertificatesTab,initializeCertificatesTab,bindCertificateActions,CertOverview,CertView};\n'+src.slice(idx);
  try{ w.eval(ICONS); w.eval(hooked);}catch(e){ console.log('LOAD ERROR',e.stack.split('\n').slice(0,4).join('\n')); }
  if(opts.oat) w.eval(fs.readFileSync(path.join(UI,'vendor','oat','oat.min.js'),'utf8'));
  return {w,d:w.document,errors,calls,json,stubs};
}
// Same sequence the service detail loader runs for the certificates tab.
async function openInstanceTab(w,d,id){
  const box=d.createElement('div'); box.id='host-'+Math.random(); d.body.appendChild(box);
  box.innerHTML=w.__T.renderServiceCertificatesTab(id);
  w.__T.bindCertificateActions(d,{
    'fetch-manifest':(i)=>w.fetchManifestCertificates(i),
    'fetch-vm':(i)=>w.fetchServiceVMCertificates(i),
    'fetch-service-endpoint':(i)=>w.fetchServiceEndpointCertificate(i)},id);
  w.__T.CertOverview.mountInstance(d.getElementById('service-cert-overview-'+id),id);
  return box;
}
return {boot,openInstanceTab,sleep,EVIL,day,now,iso};
})();

const fleet=(()=>{
const root=UI+path.sep;
const html=fs.readFileSync(root+'index.html','utf8');
const src=fs.readFileSync(root+'blacksmith.js','utf8');

const EVIL='"><img src=x onerror=alert(1)><script>alert(2)</script>\'';
const day=86400000;
const now=Date.now();
const iso=(ms)=>new Date(ms).toISOString();

function mkCert(i,o){
  const base={
    id:'c'+i,key:'/ws-bosh/'+EVIL+'/var'+i,variable:'var'+i+EVIL,kind:'leaf',common_name:'cn'+i+EVIL,issuer:'CN=Services CA'+EVIL,
    authority_key_id:'AKI1',subject_key_id:'SKI'+i,
    owners:[{kind:'instance',instance_id:'inst'+(i%3),instance_name:'owner'+(i%3)+EVIL,service_name:'valkey'+(i%2?'':EVIL),plan_name:'small',deployment:'d'}],
    affected_instances:1,effective_not_after:iso(now+ (i*9-3)*day),days_left:i*9-3,
    expiry:i===0?'expired':(i<3?'critical':(i<5?'warning':'ok')),health:i===1?'error':'ok',attention:i<3,
    observations:[{source:'credhub',owner:{kind:'instance'},locator:'/x/'+EVIL,sha256_fingerprint:'8f2e1c0aabbcc',matches:'deployed',state:'ok',observed_at:iso(now)},
      {source:'probe',owner:{kind:'instance'},locator:'10.0.0.'+i+':6379'+EVIL,state:'failed',error:'timed out'+EVIL,observed_at:iso(now)}],
    findings:[{code:'probe_failed',severity:'info',message:'node/2 did not answer'+EVIL}],
    coverage:{expected_listeners:3,observed_listeners:2,failed_listeners:1,complete:false,stale:false},
    rotation:{allowed:false,reasons:['CredHub not configured'+EVIL]},
    versions:{deployed_version_id:'abcdef123456',latest_version_id:'abcdef123456'}
  };
  return Object.assign(base,o||{});
}
function fixture(){
  const certs=[];
  for(let i=0;i<8;i++) certs.push(mkCert(i));
  certs.push(mkCert(20,{id:'ca1',kind:'ca',variable:'broker_ca'+EVIL,common_name:'Services CA'+EVIL,subject_key_id:'AKI1',authority_key_id:'',issuer:'CN=Services CA'+EVIL,owners:[{kind:'broker'}],effective_not_after:iso(now+400*day),expiry:'ok',attention:false,days_left:400}));
  certs.push(mkCert(21,{id:'unk1',expiry:'unknown',expiry_reason:'CredHub not configured',effective_not_after:undefined,days_left:undefined,attention:false,owners:[{kind:'broker'}]}));
  return certs;
}
function summarize(c){
  const n=(f)=>c.filter(f).length;
  return {certificates:c.length,affected_instances:3,expired:n(x=>x.expiry==='expired'),critical:n(x=>x.expiry==='critical'),warning:n(x=>x.expiry==='warning'),expiry_ok:n(x=>x.expiry==='ok'),expiry_unknown:n(x=>x.expiry==='unknown'),health_errors:n(x=>x.health==='error'),needs_attention:n(x=>x.attention),unconverged:0,stale:0,incomplete_coverage:c.filter(x=>!x.coverage.complete).length};
}

async function boot(opts={}){
  const dom=new JSDOM(html,{runScripts:'outside-only',pretendToBeVisual:true,url:'http://localhost/'});
  const w=dom.window;
  const stubs=installStubs(w);
  w.Element.prototype.getClientRects=function(){return [1]};
  w.matchMedia=w.matchMedia||(()=>({matches:false,addListener(){},addEventListener(){}}));
  w.navigator.clipboard={writeText:()=>Promise.resolve()};
  w.CSS={escape:(x)=>String(x).replace(/[^\w-]/g,'\\$&')};
  w.HTMLElement.prototype.scrollIntoView=function(){};
  const log=[]; const errors=[];
  watchUncaught(w,errors);
  w.console.log=()=>{}; w.console.error=(...a)=>errors.push(a.map(String).join(' '));
  const server={settings:{warning_days:30,critical_days:7,scan_interval_minutes:60,scan_concurrency:4,instance_scan_timeout_seconds:60,probe_enabled:true,pause_on_failure:true,preview_ttl_minutes:15},
    certs:fixture(), mode:'normal', calls:log, scan:{id:'s1',state:'idle',completed_at:iso(now-3600e3),next_at:iso(now+3600e3),progress:{done:5,total:5},errors:[]}, itemFail:false, caps:{credhub:{state:'not_configured',reason:'no credhub block'},director:{version:'282.0.5',force_latest_variables:true},rotation:{enabled:false,reason:'CredHub not configured'},patch_in_place:{enabled:false,reason:'x'},probe:{enabled:true}},
    putStatus:null,delay:0};
  const json=(status,body)=>Promise.resolve({ok:status>=200&&status<300,status,statusText:'',json:()=>Promise.resolve(body),text:()=>Promise.resolve(JSON.stringify(body))});
  function inventory(url){
    const u=new URL(url,'http://x'); const p=u.searchParams;
    if(server.mode==='disabled') return json(403,{success:false,error:'Certificate inventory is off on this broker (certificates.inventory.enabled).',code:'inventory_disabled'});
    if(server.mode==='fail') return json(500,{success:false,error:'boom'+EVIL,code:'internal'});
    let certs=server.certs.slice();
    if(server.recompute){ const s=server.settings; certs=certs.map(c=>typeof c.days_left==='number'?Object.assign({},c,{expiry:c.days_left<0?'expired':c.days_left<s.critical_days?'critical':c.days_left<s.warning_days?'warning':'ok'}):c); }
    const all=certs;
    const resp={generated_at:iso(now),summary:summarize(all),filtered:null,scan:server.scan,capabilities:server.caps,settings:{warning_days:server.settings.warning_days,critical_days:server.settings.critical_days},certificates:[]};
    if(server.mode==='first-failed'){ resp.summary=summarize([]);resp.scan={id:'s',state:'failed',progress:{done:0,total:0},errors:[{error:'director unreachable'+EVIL}]}; resp.filtered=resp.summary; return json(200,resp);}
    if(server.mode==='never'){ resp.summary=summarize([]);resp.scan={id:'s',state:'running',progress:{done:12,total:58},errors:[]}; resp.filtered=resp.summary; return json(200,resp);}
    if(p.get('attention')==='true') certs=certs.filter(c=>c.attention);
    const ex=p.getAll('expiry'); if(ex.length) certs=certs.filter(c=>ex.includes(c.expiry));
    const sv=p.getAll('service'); if(sv.length) certs=certs.filter(c=>c.owners.some(o=>sv.includes(o.service_name)));
    const inst=p.getAll('instance'); if(inst.length) certs=certs.filter(c=>c.owners.some(o=>inst.includes(o.instance_id)));
    if(p.get('q')) certs=certs.filter(c=>JSON.stringify(c).toLowerCase().includes(p.get('q').toLowerCase()));
    const ea=p.get('expires_after'), eb=p.get('expires_before');
    if(ea) certs=certs.filter(c=>c.effective_not_after&&new Date(c.effective_not_after)>=new Date(ea));
    if(eb) certs=certs.filter(c=>c.effective_not_after&&new Date(c.effective_not_after)<new Date(eb));
    resp.filtered=summarize(certs);
    if(p.get('summary_only')!=='true') resp.certificates=certs;
    return json(200,resp);
  }
  w.fetch=(u,o={})=>{
    u=String(u); const method=(o.method||'GET').toUpperCase(); log.push(method+' '+u);
    if(u.startsWith('/b/certificates/inventory')) return inventory(u);
    if(u==='/b/certificates/scan'&&method==='POST'){ if(server.mode==='disabled') return json(403,{code:'inventory_disabled',error:'off'}); server.scan=Object.assign({},server.scan,{state:'running',progress:{done:1,total:5}}); return json(202,{scan_id:'s2',state:'running'});}
    if(u==='/b/certificates/settings'){
      if(method==='PUT'){ const body=JSON.parse(o.body); if(server.putStatus) return json(server.putStatus.status,server.putStatus.body);
        const bad=(m)=>json(400,{success:false,error:m,code:'invalid_settings'});
        if(body.warning_days<1) return bad('warning_days must be at least 1');
        if(body.critical_days<1) return bad('critical_days must be at least 1');
        if(body.critical_days>=body.warning_days) return bad('critical_days must be below warning_days');
        if(body.scan_interval_minutes<5) return bad('scan_interval_minutes must be at least 5');
        if(body.scan_concurrency<1||body.scan_concurrency>16) return bad('scan_concurrency must be between 1 and 16');
        if(body.instance_scan_timeout_seconds<1) return bad('instance_scan_timeout_seconds must be at least 1');
        if(body.preview_ttl_minutes<5||body.preview_ttl_minutes>60) return bad('preview_ttl_minutes must be between 5 and 60');
        server.settings=body; return json(200,body);}
      if(server.settingsGet) return json(server.settingsGet.status,server.settingsGet.body);
      return json(200,server.settings);
    }
    if(u.startsWith('/b/certificates/items/')){ if(server.itemFail) return json(500,{success:false,error:'item broke'+EVIL,code:'internal'});
      const id=decodeURIComponent(u.split('/').pop()); const c=server.certs.find(x=>x.id===id); if(!c) return json(404,{success:false,error:'none',code:'not_found'});
      return json(200,Object.assign({},c,{pem:'-----BEGIN CERTIFICATE-----\n'+EVIL+'\n-----END CERTIFICATE-----',facts:Object.assign({common_name:c.common_name,issuer:c.issuer,serial:'0a'+EVIL,not_before:iso(now-day),not_after:c.effective_not_after||iso(now),dns_names:[EVIL,'a.example'],ip_addresses:['10.0.0.1'],ext_key_usages:['serverAuth'],key_algorithm:'RSA',key_bits:2048,subject_key_id:c.subject_key_id,authority_key_id:c.authority_key_id},server.itemFacts||{})}));}
    return Promise.resolve({ok:false,status:404,statusText:'nf',json:()=>Promise.resolve({}),text:()=>Promise.resolve('')});
  };
  await new Promise(r=>w.addEventListener('load',r,{once:w.document.readyState!=='complete'})||setTimeout(r,0));
  try{ w.eval(ICONS); w.eval(src); }catch(e){ console.log('LOAD ERROR',e.stack.split('\n').slice(0,4).join('\n')); }
  await sleep(50);
  return {w,d:w.document,server,log,errors,stubs};
}
const sleep=(ms)=>new Promise(r=>setTimeout(r,ms));
return {boot,sleep,EVIL,day,now,iso};
})();

module.exports = { main, instance, fleet, UI };
