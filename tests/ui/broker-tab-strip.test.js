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

// The Cloud Foundry strip under the Broker section has two tabs, Marketplace and Services, and no way to
// disable either. It is selected by the active class and its panel is shown by that class, so the checks
// below read only the tab, the panel, and the active class, and mean the same on the old strip and the new one.
const start=async()=>{
 const env=boot({noDialogs:true,routes:[['/b/status',(u,o,j)=>j(200,{env:'test',deployment:'blacksmith',instances:{},plans:{}})],['/b/config/ssh/ui-terminal-status',(u,o,j)=>j(200,{})],['/v2/catalog',(u,o,j)=>j(200,{services:[]})],['/b/cf/endpoints',(u,o,j)=>j(200,{})]]});
 envs.push(env);
 const {w,d}=env;
 w.eval(OAT);
 await sleep(60);
 const host=d.createElement('div'); host.id='broker-host'; d.body.appendChild(host);
 host.innerHTML=w.__T.renderBrokerTab();
 await w.__T.initBrokerTab();
 await sleep(20);
 env.errors.length=0;
 const tabs=[...host.querySelectorAll('[data-cftab]')];
 const panels=[...host.querySelectorAll('[data-panel]')];
 const shown=(x)=>!x.hidden&&x.style.display!=='none'&&(x.style.display==='block'||x.classList.contains('active'));
 return {...env,host,tabs,panels,selected:()=>tabs.map((x,i)=>x.classList.contains('active')?i:-1).filter(i=>i>=0),visible:()=>panels.map((x,i)=>shown(x)?i:-1).filter(i=>i>=0)};
};
const press=(w,target,key)=>target.dispatchEvent(new w.KeyboardEvent('keydown',{key,bubbles:true,cancelable:true}));

t('broker strip starts on Marketplace with its panel shown',async()=>{
 const {tabs,panels,selected,visible}=await start();
 expect(tabs.map(x=>x.dataset.cftab)).toEqual(['marketplace','services']);
 expect(panels.map(x=>x.dataset.panel)).toEqual(['marketplace','services']);
 expect(selected()).toEqual([0]);
 expect(visible()).toEqual([0]);
});

t('broker strip click on a tab shows its panel and clicking back returns',async()=>{
 const {tabs,selected,visible}=await start();
 tabs[1].click(); await sleep(5);
 expect(selected()).toEqual([1]);
 expect(visible()).toEqual([1]);
 tabs[0].click(); await sleep(5);
 expect(selected()).toEqual([0]);
 expect(visible()).toEqual([0]);
});

t('broker strip tabs report the selection to the router reader',async()=>{
 const {w,d,tabs}=await start();
 tabs[1].click(); await sleep(5);
 const sub=d.querySelector('.broker-tab-btn.active');
 expect(sub.dataset.cftab).toBe('services');
 expect(typeof w.CFEndpointManager.switchToBrokerTab).toBe('function');
 w.CFEndpointManager.switchToBrokerTab('marketplace'); await sleep(5);
 expect(d.querySelector('.broker-tab-btn.active').dataset.cftab).toBe('marketplace');
});

t('broker strip arrow keys move and wrap, with focus following',async()=>{
 const {w,d,tabs,selected,visible}=await start();
 tabs[0].focus();
 press(w,tabs[0],'ArrowLeft');
 expect(selected()).toEqual([1]); expect(d.activeElement===tabs[1]).toBe(true); expect(visible()).toEqual([1]);
 press(w,tabs[1],'ArrowRight');
 expect(selected()).toEqual([0]); expect(d.activeElement===tabs[0]).toBe(true); expect(visible()).toEqual([0]);
 press(w,tabs[0],'ArrowRight');
 expect(selected()).toEqual([1]); expect(d.activeElement===tabs[1]).toBe(true);
});

t('broker strip Home and End go to the first and last tab',async()=>{
 const {w,d,tabs,selected,visible}=await start();
 tabs[0].focus();
 press(w,tabs[0],'End');
 expect(selected()).toEqual([1]); expect(d.activeElement===tabs[1]).toBe(true); expect(visible()).toEqual([1]);
 press(w,tabs[1],'Home');
 expect(selected()).toEqual([0]); expect(d.activeElement===tabs[0]).toBe(true); expect(visible()).toEqual([0]);
});

t('broker strip is a bs-tabs element with the tab list and panels as its children',async()=>{
 const {host,tabs,panels}=await start();
 const strip=host.querySelector('bs-tabs');
 expect(strip).not.toBeNull();
 const kids=[...strip.children];
 expect(kids[0].getAttribute('role')).toBe('tablist');
 expect(kids.slice(1).every(x=>x.getAttribute('role')==='tabpanel')).toBe(true);
 expect(kids.slice(1)).toEqual(panels);
 tabs.forEach((tab,i)=>expect(panels[i].id===tab.getAttribute('aria-controls')).toBe(true));
 expect(tabs.map(x=>x.getAttribute('aria-selected'))).toEqual(['true','false']);
 expect(tabs.map(x=>x.tabIndex)).toEqual([0,-1]);
 expect(panels.map(x=>x.hidden)).toEqual([false,true]);
 expect(panels.filter(x=>/display/.test(x.getAttribute('style')||''))).toEqual([]);
 expect(host.querySelector('.broker-tab-content')).toBeNull();
});
