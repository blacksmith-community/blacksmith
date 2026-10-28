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

// bs-tabs extends Oat's ot-tabs with Home, End, and disabled tabs.
const start=async(markup)=>{
 const env=boot({noDialogs:true,routes:[['/b/status',(u,o,j)=>j(200,{env:'test',deployment:'blacksmith',instances:{},plans:{}})],['/b/config/ssh/ui-terminal-status',(u,o,j)=>j(200,{})],['/v2/catalog',(u,o,j)=>j(200,{services:[]})]]});envs.push(env);
 const {w,d}=env;
 w.eval(OAT);
 await sleep(30);
 d.body.insertAdjacentHTML('beforeend',markup);
 await sleep(10);
 const host=d.getElementById('strip');
 const tabs=[...host.querySelectorAll('[role=tab]')];
 const panels=[...host.querySelectorAll('[role=tabpanel]')];
 const press=(key,target=d.activeElement)=>target.dispatchEvent(new w.KeyboardEvent('keydown',{key,bubbles:true,cancelable:true}));
 const selected=()=>tabs.filter(x=>x.getAttribute('aria-selected')==='true').map(x=>x.dataset.tab);
 return {...env,host,tabs,panels,press,selected};
};
const strip=(extra={})=>`<bs-tabs id="strip"><div role="tablist" aria-label="Fixture">${['one','two','three','four'].map(n=>`<button type="button" role="tab" data-tab="${n}" ${extra[n]||''}>${n}</button>`).join('')}</div>${['one','two','three','four'].map(n=>`<div role="tabpanel" data-tab="${n}">${n} panel</div>`).join('')}</bs-tabs>`;

t('the element upgrades, keeps Oat ids, aria-selected, roving tabindex, and shows one panel',async()=>{
 const env=await start(strip()); const {w,host,tabs,panels,selected}=env;
 expect(host instanceof w.customElements.get('ot-tabs')).toBe(true);
 expect(selected()).toEqual(['one']);
 expect(tabs.map(x=>x.tabIndex)).toEqual([0,-1,-1,-1]);
 tabs.forEach((tab,i)=>{ expect(tab.id).toBeTruthy(); expect(tab.getAttribute('aria-controls')).toBe(panels[i].id); expect(panels[i].getAttribute('aria-labelledby')).toBe(tab.id); });
 expect(panels.map(x=>x.hidden)).toEqual([false,true,true,true]);
 expect(tabs.map(x=>x.classList.contains('active'))).toEqual([true,false,false,false]);
 expect(panels.map(x=>x.classList.contains('active'))).toEqual([true,false,false,false]);
});

t('arrow keys wrap and move focus, and activation emits ot-tab-change',async()=>{
 const env=await start(strip()); const {w,d,host,tabs,press,selected}=env;
 const seen=[]; host.addEventListener('ot-tab-change',e=>seen.push(e.detail.index));
 tabs[0].focus(); press('ArrowRight');
 expect(selected()).toEqual(['two']); expect(d.activeElement).toBe(tabs[1]); expect(tabs[1].tabIndex).toBe(0); expect(tabs[0].tabIndex).toBe(-1);
 press('ArrowLeft'); press('ArrowLeft');
 expect(selected()).toEqual(['four']); expect(d.activeElement).toBe(tabs[3]);
 press('ArrowRight');
 expect(selected()).toEqual(['one']);
 expect(seen).toEqual([1,0,3,0]);
});

t('Home and End move to the first and last tab',async()=>{
 const env=await start(strip()); const {d,tabs,press,selected}=env;
 tabs[1].click(); tabs[1].focus();
 press('End'); expect(selected()).toEqual(['four']); expect(d.activeElement).toBe(tabs[3]);
 press('Home'); expect(selected()).toEqual(['one']); expect(d.activeElement).toBe(tabs[0]);
});

t('a click selects the tab and shows its panel',async()=>{
 const env=await start(strip()); const {tabs,panels,selected}=env;
 tabs[2].click();
 expect(selected()).toEqual(['three']);
 expect(panels.map(x=>x.hidden)).toEqual([true,true,false,true]);
 expect(panels.map(x=>x.classList.contains('active'))).toEqual([false,false,true,false]);
});

t('arrow keys, Home, End, and clicks skip tabs marked disabled or aria-disabled',async()=>{
 const env=await start(strip({one:'disabled',three:'aria-disabled="true"'})); const {d,host,tabs,press,selected}=env;
 // Oat opens on the first selected tab, so start from the first enabled one.
 host.select('two'); tabs[1].focus();
 expect(selected()).toEqual(['two']);
 press('ArrowRight'); expect(selected()).toEqual(['four']);
 press('ArrowRight'); expect(selected()).toEqual(['two']);
 press('ArrowLeft'); expect(selected()).toEqual(['four']);
 press('Home'); expect(selected()).toEqual(['two']);
 press('End'); expect(selected()).toEqual(['four']);
 tabs[2].click(); expect(selected()).toEqual(['four']);
 expect(host.select('three')).toBe(false); expect(host.select('one')).toBe(false);
 expect(host.select('two')).toBe(true); expect(selected()).toEqual(['two']);
});

t('End skips a disabled last tab and Home skips a disabled first tab',async()=>{
 const env=await start(strip({four:'disabled'})); const {tabs,press,selected}=env;
 tabs[0].focus();
 press('End'); expect(selected()).toEqual(['three']);
 const env2=await start(strip({one:'aria-disabled="true"'})); env2.host.select('three'); env2.tabs[2].focus();
 env2.press('Home'); expect(env2.selected()).toEqual(['two']);
});

t('a nested strip changes only its own active classes',async()=>{
 const inner=`<bs-tabs id="inner"><div role="tablist" aria-label="Inner">${['a','b','c'].map(n=>`<button type="button" role="tab" data-tab="${n}">${n}</button>`).join('')}</div>${['a','b','c'].map(n=>`<div role="tabpanel" data-tab="${n}">${n}</div>`).join('')}</bs-tabs>`;
 const outer=`<bs-tabs id="strip"><div role="tablist" aria-label="Outer"><button type="button" role="tab" data-tab="x">x</button><button type="button" role="tab" data-tab="y">y</button></div><div role="tabpanel" data-tab="x">${inner}</div><div role="tabpanel" data-tab="y">y</div></bs-tabs>`;
 const env=await start(outer); const {d,host}=env;
 const innerTabs=[...d.getElementById('inner').querySelectorAll('[role=tab]')];
 innerTabs[2].click();
 const outerTabs=[...host.querySelectorAll(':scope > [role=tablist] [role=tab]')];
 const outerPanels=[...host.querySelectorAll(':scope > [role=tabpanel]')];
 expect(outerTabs.map(x=>x.classList.contains('active'))).toEqual([true,false]);
 expect(outerPanels.map(x=>x.classList.contains('active'))).toEqual([true,false]);
 expect(innerTabs.map(x=>x.classList.contains('active'))).toEqual([false,false,true]);
});

t('select picks a tab by name, and the tab list answers blacksmithSelect',async()=>{
 const env=await start(strip()); const {host,selected}=env;
 expect(host.select('three')).toBe(true); expect(selected()).toEqual(['three']);
 expect(host.select('missing')).toBe(false);
 expect(host.querySelector('[role=tablist]').blacksmithSelect('four')).toBe(true);
 expect(selected()).toEqual(['four']);
});

t('a tab change after the strip is ready asks the route to update, and the first render does not',async()=>{
 const env=boot({noDialogs:true,routes:[['/b/status',(u,o,j)=>j(200,{env:'test',deployment:'blacksmith',instances:{},plans:{}})],['/b/config/ssh/ui-terminal-status',(u,o,j)=>j(200,{})],['/v2/catalog',(u,o,j)=>j(200,{services:[]})]]});envs.push(env);
 const {w,d}=env; w.eval(OAT); await sleep(60);
 let notified=0; const real=w.blacksmithViews.notify; w.blacksmithViews.notify=(...a)=>{notified++;return real(...a);};
 d.body.insertAdjacentHTML('beforeend',strip()); await sleep(10);
 
 expect(notified).toBe(0);
 d.querySelectorAll("#strip [role=tab]")[1].click();
 expect(notified).toBe(1);
});

t('arrow keys with Alt, Ctrl, or Meta held are left to the browser',async()=>{
 const env=await start(strip()); const {w,tabs,selected}=env;
 tabs[0].focus();
 ['altKey','ctrlKey','metaKey'].forEach(mod=>{
  const event=new w.KeyboardEvent('keydown',{key:'ArrowRight',bubbles:true,cancelable:true,[mod]:true});
  tabs[0].dispatchEvent(event);
  expect(event.defaultPrevented).toBe(false);
  expect(selected()).toEqual(['one']);
 });
});
