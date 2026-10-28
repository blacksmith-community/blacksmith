const {test,expect,afterEach}=require('bun:test');
const fs=require('fs');
const path=require('path');
const H=require('./certificates/harness.js');
const {main:{boot,sleep}}=H;
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const OAT=fs.readFileSync(path.resolve(__dirname,'..','..','ui','vendor','oat','oat.min.js'),'utf8');

const envs=[];
afterEach(()=>{
 while(envs.length){ const env=envs.pop(); try{ expect(env.alerts||[]).toEqual([]); expect(env.errors).toEqual([]); } finally { env.w.close(); } }
});

// Keyboard and activation behavior shared by every tab strip. The same
// checks run against each strip, so they hold before and after it moves
// onto the tabs element.
const start=async()=>{
 const env=boot({noDialogs:true,routes:[['/b/status',(u,o,j)=>j(200,{env:'test',deployment:'blacksmith',instances:{},plans:{}})],['/b/config/ssh/ui-terminal-status',(u,o,j)=>j(200,{})],['/v2/catalog',(u,o,j)=>j(200,{services:[]})]]});envs.push(env);
 env.w.eval(OAT);
 await sleep(60);
 return env;
};
const press=(w,target,key)=>target.dispatchEvent(new w.KeyboardEvent('keydown',{key,bubbles:true,cancelable:true}));

const manifest={text:'name: test',parsed:{name:'test',releases:[{name:'release',version:'1'}],stemcells:[{alias:'default',os:'ubuntu',version:'1'}],update:{canaries:1}}};
const inventory=()=>({generated_at:new Date().toISOString(),summary:{certificates:0},scan:{state:'idle'},capabilities:{},certificates:[],rotatable_variables:[],instance:{id:'i1',name:'valkey-1',deployment:'d',service_name:'valkey',plan_name:'small'}});
const strips={
 manifest:{
  min:4,keyboard:true,
  render:({w,d})=>{ d.body.insertAdjacentHTML('beforeend','<div id="strip-host">'+w.__T.formatManifestDetails(manifest,'instance')+'</div>'); return d.getElementById('strip-host'); },
  tabs:(root)=>[...root.querySelectorAll('[role=tab]')],
  panes:(root)=>[...root.querySelectorAll('[role=tabpanel]')]
 },
 config:{
  min:2,keyboard:true,
  render:async({w,d})=>{
   w.__routes.push([/\/b\/configs\/cloud\/[^/]+\/versions/,(u,o,j)=>j(200,{configs:[{id:'3'},{id:'2'},{id:'1'}]})],[/\/b\/configs\/[123]$/,(u,o,j)=>j(200,{content:'version '+u.split('/').pop()})]);
   await w.showConfigVersions('cloud','alpha','3'); await sleep(40);
   return d.getElementById('config-details-modal');
  },
  tabs:(root)=>[...root.querySelectorAll('.manifest-details-container [role=tab]')],
  panes:(root)=>[...root.querySelectorAll('.manifest-details-container [role=tabpanel]')]
 }
};

strips.certMain={
 min:4,keyboard:true,
 boot:async()=>{ const env=H.instance.boot({oat:true}); envs.push(env); await sleep(60); env.errors.length=0; return env; },
 render:async({w,d})=>{
  w.blacksmithData={env:'ws'};
  w.__routes.push([/certificates\/inventory/,(u,o,j)=>j(200,inventory())]);
  const host=d.createElement('div'); host.id='strip-host'; d.body.appendChild(host);
  host.innerHTML=w.__T.renderCertificatesTab(); w.__T.initializeCertificatesTab(); await sleep(80);
  return host;
 },
 tabs:(root)=>[...root.querySelectorAll('#cert-main-tablist [role=tab]')],
 panes:(root)=>[...root.querySelectorAll('[role=tabpanel]')]
};
strips.serviceCert={
 min:4,keyboard:true,
 boot:async()=>{ const env=H.instance.boot({oat:true}); envs.push(env); await sleep(60); env.errors.length=0; return env; },
 render:async({w,d})=>{
  w.__routes.push([/certificates\/instances\//,(u,o,j)=>j(200,inventory())]);
  const host=await H.instance.openInstanceTab(w,d,'i1'); host.id='strip-host'; await sleep(80);
  return host;
 },
 tabs:(root)=>[...root.querySelectorAll('#service-cert-tablist-i1 [role=tab]')],
 panes:(root)=>[...root.querySelectorAll('[role=tabpanel]')]
};
strips.inspector={
 min:3,keyboard:true,
 render:({w,d})=>{ w.inspectCertificate({subject:'CN=broker.example',issuer:'CN=Example CA',pemEncoded:'-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----',textDetails:'Certificate:\n  Data:'}); return d.querySelector('dialog[id^="cert-modal-"]'); },
 tabs:(root)=>[...root.querySelectorAll('.cert-tab-btn')],
 panes:(root)=>[...root.querySelectorAll('.cert-tab-pane')]
};

Object.entries(strips).forEach(([name,strip])=>{
 const setup=async()=>{
  const env=strip.boot?await strip.boot():await start();
  const root=await strip.render(env); await sleep(20);
  const tabs=strip.tabs(root); const panes=strip.panes(root);
  const selected=()=>tabs.filter(x=>x.getAttribute('aria-selected')==='true').map(x=>tabs.indexOf(x));
  // The older strips show a pane with an inline display, the newer ones with the active class and the hidden attribute.
  const shown=(x)=>!x.hidden&&x.style.display!=='none'&&(x.style.display==='block'||x.classList.contains('active'));
  const active=()=>panes.map((x,i)=>shown(x)?i:-1).filter(i=>i>=0);
  return {...env,root,tabs,panes,selected,active};
 };
 t(`${name} strip starts with the first tab selected and one active pane`,async()=>{
  const {tabs,active,selected}=await setup();
  expect(tabs.length).toBeGreaterThanOrEqual(strip.min);
  expect(selected()).toEqual([0]);
  expect(tabs.map(x=>x.tabIndex)).toEqual(tabs.map((x,i)=>i===0?0:-1));
  expect(active()).toEqual([0]);
 });
 t(`${name} strip clicks activate the tab and its pane`,async()=>{
  const {tabs,active,selected}=await setup();
  const at=tabs.length>2?2:1;
  tabs[at].click(); await sleep(5);
  expect(selected()).toEqual([at]);
  expect(tabs[at].classList.contains('active')).toBe(true);
  expect(tabs[0].classList.contains('active')).toBe(false);
  expect(active()).toEqual([at]);
 });
 (strip.keyboard?t:test.skip)(`${name} strip arrow keys move and wrap, with focus following`,async()=>{
  const {w,d,tabs,selected}=await setup();
  tabs[0].focus();
  press(w,tabs[0],'ArrowLeft');
  expect(selected()).toEqual([tabs.length-1]); expect(d.activeElement===tabs[tabs.length-1]).toBe(true);
  press(w,tabs[tabs.length-1],'ArrowRight');
  expect(selected()).toEqual([0]); expect(d.activeElement===tabs[0]).toBe(true);
  press(w,tabs[0],'ArrowRight');
  expect(selected()).toEqual([1]); expect(d.activeElement===tabs[1]).toBe(true);
 });
 (strip.keyboard?t:test.skip)(`${name} strip Home and End go to the first and last tab`,async()=>{
  const {w,d,tabs,selected}=await setup();
  tabs[0].focus();
  press(w,tabs[0],'End');
  expect(selected()).toEqual([tabs.length-1]); expect(d.activeElement===tabs[tabs.length-1]).toBe(true);
  press(w,tabs[tabs.length-1],'Home');
  expect(selected()).toEqual([0]); expect(d.activeElement===tabs[0]).toBe(true);
 });
});

// Disabled tabs are skipped by every strip on the tabs element. The older
// strips had no disabled handling, so these have no before-run.
['manifest','config','certMain','serviceCert','inspector'].forEach((name)=>{
 t(`${name} strip arrow keys and End skip a disabled last tab`,async()=>{
  const strip=strips[name]; const env=strip.boot?await strip.boot():await start(); const root=await strip.render(env); await sleep(20);
  const tabs=strip.tabs(root); const last=tabs.length-1;
  const selected=()=>tabs.filter(x=>x.getAttribute('aria-selected')==='true').map(x=>tabs.indexOf(x));
  tabs[last].disabled=true; tabs[0].focus();
  press(env.w,tabs[0],'End'); expect(selected()).toEqual([last-1]);
  press(env.w,tabs[last-1],'Home'); expect(selected()).toEqual([0]);
  press(env.w,tabs[0],'ArrowLeft'); expect(selected()).toEqual([last-1]);
  tabs[last].click(); expect(selected()).toEqual([last-1]);
 });
});

// A strip on bs-tabs keeps its tab list and panels as direct children, in
// the same order, so Oat can pair them by index.
['manifest','config','certMain','serviceCert','inspector'].forEach((name)=>{
 t(`${name} strip is a bs-tabs element with the tab list and panels as its children`,async()=>{
  const env=strips[name].boot?await strips[name].boot():await start(); const root=await strips[name].render(env); await sleep(20);
  const host=root.querySelector('bs-tabs');
  expect(host).not.toBeNull();
  const kids=[...host.children];
  expect(kids[0].getAttribute('role')).toBe('tablist');
  expect(kids.slice(1).every(x=>x.getAttribute('role')==='tabpanel')).toBe(true);
  const tabs=strips[name].tabs(root);
  expect(kids.length-1).toBe(tabs.length);
  tabs.forEach((tab,i)=>expect(kids[i+1].id===tab.getAttribute('aria-controls')).toBe(true));
  expect(root.querySelector('.manifest-tab-content')).toBeNull();
  expect(kids.filter(x=>/display/.test(x.getAttribute('style')||''))).toEqual([]);
 });
 t(`${name} strip hides inactive panels with the hidden attribute`,async()=>{
  const env=strips[name].boot?await strips[name].boot():await start(); const root=await strips[name].render(env); await sleep(20);
  const panes=strips[name].panes(root);
  expect(panes.map(x=>x.hidden)).toEqual(panes.map((x,i)=>i!==0));
 });
});
