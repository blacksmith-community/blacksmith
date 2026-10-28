const {test,expect,afterEach}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const OAT=fs.readFileSync(path.resolve(__dirname,'..','..','ui','vendor','oat','oat.min.js'),'utf8');

const envs=[];
afterEach(()=>{
 while(envs.length){ const env=envs.pop(); try{ expect(env.alerts).toEqual([]); expect(env.errors).toEqual([]); } finally { env.w.close(); } }
});

// The config details view is a native <dialog> opened with showModal. Every way out must leave the surface stack, inert, and
// aria-hidden as they were, and must put focus back on the opener.
const start=async(opts={})=>{
 const env=boot({noDialogs:true,routes:[['/b/configs/42',(u,o,j)=>j(200,{id:'42',name:'default',type:'cloud',team:'t',created_at:'2026-10-24T10:00:00Z',content:'azs: []',metadata:{}})],['/b/configs/cloud/default/versions',(u,o,j)=>j(200,{configs:[{id:'42*',name:'default',type:'cloud'}],count:1})],['/b/status',(u,o,j)=>j(200,{env:'test',deployment:'blacksmith',instances:{},plans:{}})],['/b/config/ssh/ui-terminal-status',(u,o,j)=>j(200,{})],['/v2/catalog',(u,o,j)=>j(200,{services:[]})]]});envs.push(env);
 const {w,d}=env;
 if(!opts.noOat) w.eval(OAT);
 const opener=d.createElement('button');opener.id='config-opener';opener.textContent='Config';d.body.appendChild(opener);
 await sleep(60);
 opener.focus();
 const dialog=()=>d.getElementById('config-details-modal');
 const snapshot=()=>JSON.stringify({
  surfaces:w.blacksmithSurfaces.size(),
  inert:[...d.querySelectorAll('[inert]')].map(n=>n.id||n.tagName),
  hidden:[...d.querySelectorAll('[aria-hidden]')].filter(n=>!n.closest('dialog')).map(n=>(n.id||n.tagName)+'='+n.getAttribute('aria-hidden'))
 });
 const key=(k)=>d.activeElement.dispatchEvent(new w.KeyboardEvent('keydown',{key:k,bubbles:true,cancelable:true}));
 const backdrop=()=>dialog().dispatchEvent(new w.MouseEvent('click',{bubbles:true,cancelable:true}));
 const touch=()=>dialog().dispatchEvent(new w.Event('touchstart',{bubbles:true,cancelable:true}));
 return {...env,opener,dialog,snapshot,key,backdrop,touch};
};
const openIt=async(env)=>{ const p=env.w.showConfigVersions('cloud','default','42'); await sleep(60); return p; };
const closeIt=(env)=>env.w.hideConfigVersionsModal();

t('the config details view stays usable when the tabs element never loads',async()=>{
 const env=await start({noOat:true}); const {w,d,dialog,stubs}=env;
 expect(w.customElements.get('ot-tabs')).toBeUndefined();
 await openIt(env);
 expect(dialog().open).toBe(true);
 expect(d.getElementById('versions-panel')).not.toBeNull();
 expect(d.querySelector('#config-details-modal .config-versions, #config-details-modal table')).not.toBeNull();
 w.blacksmithViews.activate({screen:'blacksmith',section:'manifest',subview:'versions'}).catch(()=>{});
 closeIt(env); await stubs.settle();
 expect(dialog().open).toBe(false);
});

t('a route subview names the missing detail view when the tabs element never loads',async()=>{
 const env=await start({noOat:true}); const {w,d}=env;
 w.__routes.push(['/b/deployments/blacksmith/manifest',(u,o,j)=>j(200,{text:'name: test',parsed:{name:'test',releases:[{name:'release',version:'1'}],stemcells:[],update:{canaries:1}}})]);
 expect(w.customElements.get('bs-tabs')).toBeUndefined();
 let failure=null;
 try{ await w.blacksmithViews.activate({screen:'blacksmith',section:'manifest',subview:'versions'}); }catch(e){ failure=e; }
 expect(d.querySelector('#blacksmith .manifest-details-container bs-tabs')).not.toBeNull();
 expect(failure&&failure.message).toBe('This detail view is not available for the selected resource.');
});

t('the config details view opens as a native modal dialog on the surface stack',async()=>{
 const env=await start(); const {w,d,dialog,stubs,snapshot}=env;
 const before=snapshot();
 await openIt(env);
 expect(dialog() instanceof w.HTMLDialogElement).toBe(true);
 expect(dialog().open).toBe(true);
 expect(stubs.isModal(dialog())).toBe(true);
 expect(dialog().getAttribute('aria-labelledby')).toBe('config-details-modal-title');
 expect(dialog().querySelector('.modal-content')).toBeNull();
 expect(dialog().hasAttribute('aria-hidden')).toBe(false);
 expect(w.blacksmithSurfaces.size()).toBe(1);
 expect(dialog().contains(d.activeElement)).toBe(true);
 expect(d.body.style.overflow).toBe('hidden');
 expect(d.getElementById('config-name').textContent).toBe('default');
 closeIt(env); await stubs.settle();
 expect(snapshot()).toBe(before);
});

const paths=[
 {name:'Escape',close:({key})=>key('Escape')},
 {name:'a click on the backdrop',close:({backdrop})=>backdrop()},
 {name:'a touch on the backdrop',close:({touch})=>touch()},
 {name:'the close button',close:(env)=>env.dialog().querySelector('.modal-close').click()}
];
paths.forEach(({name,close})=>{
 t(`${name} closes the config details view and restores the stack, inert, and focus`,async()=>{
  const env=await start(); const {w,d,dialog,stubs,snapshot}=env;
  const before=snapshot();
  await openIt(env);
  expect(snapshot()).not.toBe(before);
  close(env);
  await stubs.settle();
  expect(dialog().open).toBe(false);
  expect(stubs.isModal(dialog())).toBe(false);
  expect(snapshot()).toBe(before);
  expect(d.activeElement).toBe(env.opener);
  expect(d.body.style.overflow).toBe('');
  expect(w.currentConfigInfo).toBeNull();
 });
});

t('a click or touch inside the config details view leaves it open',async()=>{
 const env=await start(); const {w,d,dialog,stubs}=env;
 await openIt(env);
 const inside=d.getElementById('config-details-content');
 inside.click();
 const e=new w.Event('touchstart',{bubbles:true,cancelable:true});
 inside.dispatchEvent(e);
 await stubs.settle();
 expect(e.defaultPrevented).toBe(false);
 expect(dialog().open).toBe(true);
 closeIt(env); await stubs.settle();
});

t('a confirmation opened over the config details view returns to it, still modal',async()=>{
 const env=await start(); const {w,d,dialog,stubs,key}=env;
 await openIt(env);
 const done=w.showConfirmDialog({title:'Sure',message:'Go on?'});
 await sleep(10);
 expect(w.blacksmithSurfaces.size()).toBe(2);
 key('Escape');
 expect(await done).toBe(false);
 await stubs.settle();
 expect(dialog().open).toBe(true);
 expect(stubs.isModal(dialog())).toBe(true);
 expect(dialog().hasAttribute('inert')).toBe(false);
 expect(w.blacksmithSurfaces.size()).toBe(1);
 key('Escape'); await stubs.settle();
 expect(dialog().open).toBe(false);
 expect(w.blacksmithSurfaces.size()).toBe(0);
});

t('the static config dialog also opens natively from the config ID link and closes with Escape',async()=>{
 const env=await start(); const {w,d,dialog,stubs,snapshot,key}=env;
 const before=snapshot();
 w.showConfigDetails('42'); await sleep(60);
 expect(dialog().open).toBe(true);
 expect(stubs.isModal(dialog())).toBe(true);
 expect(d.getElementById('config-name').textContent).toBe('default');
 key('Escape'); await stubs.settle();
 expect(dialog().open).toBe(false);
 expect(snapshot()).toBe(before);
 expect(d.activeElement).toBe(env.opener);
});
