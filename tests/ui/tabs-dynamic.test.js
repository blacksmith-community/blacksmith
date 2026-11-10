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

// A bs-tabs element can start empty and gain or lose tabs afterwards, which
// the terminal dialog needs because sessions come and go. Each tab sits in a
// presentation wrapper beside its own extra controls, and Oat's click
// handler must only react to the role=tab button.
const start=async(markup)=>{
 const env=boot({noDialogs:true,routes:[['/b/status',(u,o,j)=>j(200,{env:'test',deployment:'blacksmith',instances:{},plans:{}})],['/b/config/ssh/ui-terminal-status',(u,o,j)=>j(200,{})],['/v2/catalog',(u,o,j)=>j(200,{services:[]})]]});envs.push(env);
 const {w,d}=env;
 w.eval(OAT);
 await sleep(30);
 d.body.insertAdjacentHTML('beforeend',markup);
 await sleep(10);
 const host=d.getElementById('dyn');
 const list=host.querySelector('[role=tablist]');
 let n=0;
 const make=(name)=>{
  const entry=d.createElement('div'); entry.setAttribute('role','presentation'); entry.dataset.entry=name;
  const tab=d.createElement('button'); tab.type='button'; tab.setAttribute('role','tab'); tab.dataset.tab=name; tab.textContent=name;
  const extra=d.createElement('button'); extra.type='button'; extra.className='extra'; extra.textContent='x';
  entry.append(tab,extra);
  const panel=d.createElement('div'); panel.setAttribute('role','tabpanel'); panel.dataset.tab=name; panel.textContent=name+' panel';
  return {entry,tab,extra,panel,id:++n};
 };
 const names=()=>host.tabs.map(x=>x.dataset.tab);
 const selected=()=>host.tabs.filter(x=>x.getAttribute('aria-selected')==='true').map(x=>x.dataset.tab);
 const shown=()=>host.panels.filter(x=>!x.hidden).map(x=>x.dataset.tab);
 return {...env,host,list,make,names,selected,shown};
};
const empty='<bs-tabs id="dyn"><div role="tablist" aria-label="Dynamic"></div></bs-tabs>';

t('an empty tab set starts quietly and takes its first tab as selected',async()=>{
 const env=await start(empty); const {host,make,selected,shown}=env;
 expect(host.tabs).toEqual([]);
 const a=make('a');
 host.addTab(a.entry,a.tab,a.panel);
 expect(selected()).toEqual(['a']);
 expect(shown()).toEqual(['a']);
 expect(a.tab.classList.contains('active')).toBe(true);
 expect(a.panel.classList.contains('active')).toBe(true);
 expect(a.tab.getAttribute('aria-controls')).toBe(a.panel.id);
 expect(a.panel.getAttribute('aria-labelledby')).toBe(a.tab.id);
});

t('a tab added later can be selected by click and by activeIndex',async()=>{
 const env=await start(empty); const {host,make,selected,shown}=env;
 const [a,b,c]=['a','b','c'].map(make);
 host.addTab(a.entry,a.tab,a.panel); host.addTab(b.entry,b.tab,b.panel); host.addTab(c.entry,c.tab,c.panel);
 expect(selected()).toEqual(['a']);
 b.tab.click();
 expect(selected()).toEqual(['b']); expect(shown()).toEqual(['b']);
 host.activeIndex=2;
 expect(selected()).toEqual(['c']); expect(shown()).toEqual(['c']);
 expect(host.activeIndex).toBe(2);
});

t('adding a tab with select makes it the selected one and others stay hidden',async()=>{
 const env=await start(empty); const {host,make,selected,shown}=env;
 const [a,b]=['a','b'].map(make);
 host.addTab(a.entry,a.tab,a.panel);
 host.addTab(b.entry,b.tab,b.panel,{select:true});
 expect(selected()).toEqual(['b']); expect(shown()).toEqual(['b']);
 expect(a.tab.tabIndex).toBe(-1); expect(b.tab.tabIndex).toBe(0);
});

t('removing the selected tab selects its neighbour and removes its wrapper and panel',async()=>{
 const env=await start(empty); const {host,make,names,selected,shown}=env;
 const [a,b,c]=['a','b','c'].map(make);
 [a,b,c].forEach(x=>host.addTab(x.entry,x.tab,x.panel));
 b.tab.click();
 expect(host.removeTab(b.tab)).toBe(true);
 expect(names()).toEqual(['a','c']);
 expect(b.entry.isConnected).toBe(false); expect(b.panel.isConnected).toBe(false);
 expect(selected()).toEqual(['c']); expect(shown()).toEqual(['c']);
 expect(host.removeTab(c.tab)).toBe(true);
 expect(selected()).toEqual(['a']); expect(shown()).toEqual(['a']);
});

t('removing an unselected tab keeps the selection',async()=>{
 const env=await start(empty); const {host,make,selected}=env;
 const [a,b,c]=['a','b','c'].map(make);
 [a,b,c].forEach(x=>host.addTab(x.entry,x.tab,x.panel));
 c.tab.click();
 host.removeTab(a.tab);
 expect(selected()).toEqual(['c']);
 expect(host.removeTab(a.tab)).toBe(false);
});

t('removing every tab and adding one again works, and arrow keys reach the new tabs',async()=>{
 const env=await start(empty); const {w,d,host,make,selected,shown}=env;
 const a=make('a'); host.addTab(a.entry,a.tab,a.panel);
 host.removeTab(a.tab);
 expect(host.tabs).toEqual([]);
 const [b,c]=['b','c'].map(make);
 host.addTab(b.entry,b.tab,b.panel); host.addTab(c.entry,c.tab,c.panel);
 expect(selected()).toEqual(['b']);
 b.tab.focus();
 b.tab.dispatchEvent(new w.KeyboardEvent('keydown',{key:'ArrowRight',bubbles:true,cancelable:true}));
 expect(selected()).toEqual(['c']); expect(shown()).toEqual(['c']);
 expect(d.activeElement).toBe(c.tab);
});

t('a click on a control beside a tab does not select that tab',async()=>{
 const env=await start(empty); const {host,make,selected}=env;
 const [a,b]=['a','b'].map(make);
 host.addTab(a.entry,a.tab,a.panel); host.addTab(b.entry,b.tab,b.panel);
 b.extra.click();
 expect(selected()).toEqual(['a']);
});

t('an element marked local does not announce route changes',async()=>{
 const env=await start('<bs-tabs id="dyn" data-local><div role="tablist"></div></bs-tabs>'); const {w,host,make}=env;
 let notified=0; w.blacksmithViews={...(w.blacksmithViews||{}),notify:()=>{notified++;}};
 const [a,b]=['a','b'].map(make);
 host.addTab(a.entry,a.tab,a.panel); host.addTab(b.entry,b.tab,b.panel);
 b.tab.click();
 expect(notified).toBe(0);
});
