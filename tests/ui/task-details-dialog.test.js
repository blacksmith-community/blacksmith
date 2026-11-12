const {test,expect,afterEach}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const { pinZone } = require('./fixed-zone.js');
pinZone('UTC');
const t=JSDOM?test:test.skip;

const UI=path.resolve(__dirname,'..','..','ui');
const OAT=fs.readFileSync(path.join(UI,'vendor','oat','oat.min.js'),'utf8');
const strip=(css)=>css.replace(/\/\*[\s\S]*?\*\//g,'');
const bodies=(file,selector)=>{
 const out=[];
 strip(fs.readFileSync(path.join(UI,file),'utf8')).replace(/([^{}]+)\{([^{}]*)\}/g,(m,sel,body)=>{
  if(sel.split(',').map((s)=>s.replace(/\s+/g,' ').trim()).includes(selector)) out.push(body);
  return m;
 });
 return out.join(';');
};

const envs=[];
afterEach(()=>{ while(envs.length) envs.pop().w.close(); });

// 1761927378 is 2025-10-31T16:16:18Z.
const T0=1761927378;
const line=(extra)=>JSON.stringify(Object.assign({time:T0,stage:'Preparing deployment',task:'',state:'started'},extra));
const start=async(state,lines)=>{
 const output=lines.join('\n');
 const env=boot({noDialogs:true,routes:[
  ['/b/tasks/42/output',(u,o,j)=>j(200,{output})],
  ['/b/tasks/42',(u,o,j)=>j(200,{task:{id:42,state,description:'create deployment',deployment:'d',user:'admin',started_at:'2026-10-31T20:16:18Z',ended_at:'2026-10-31T20:18:09Z'}})],
  ['/b/status',(u,o,j)=>j(200,{env:'test',deployment:'blacksmith',instances:{},plans:{}})],
  ['/b/config/ssh/ui-terminal-status',(u,o,j)=>j(200,{})],
  ['/v2/catalog',(u,o,j)=>j(200,{services:[]})]]});
 envs.push(env);
 env.w.eval(OAT);
 await sleep(60);
 const p=env.w.showTaskDetails(42);
 await sleep(60);
 await p;
 return env;
};
const ALL=[line(),line({time:T0+1,stage:'Compiling packages',state:'in_progress',progress:50}),line({time:T0+2,state:'finished'}),line({time:T0+3,state:'failed',error:{code:1,message:'boom'}})];

t('the title names the BOSH task by its number',async()=>{
 const {d}=await start('done',ALL);
 expect(d.getElementById('task-details-modal-title').textContent).toBe('BOSH task 42');
});

t('the output views are a real tab list that the arrow keys move through',async()=>{
 const {w,d}=await start('done',ALL);
 const strip=d.querySelector('#task-details-content bs-tabs');
 expect(strip).not.toBeNull();
 const list=strip.querySelector(':scope > [role="tablist"]');
 expect(list).not.toBeNull();
 expect(list.classList.contains('task-tabs-nav')).toBe(true);
 const tabs=[...list.querySelectorAll('[role="tab"]')];
 expect(tabs.map((x)=>x.textContent.trim())).toEqual(['Task','Debug','Raw','CPI']);
 for(const tab of tabs){
  const panel=d.getElementById(tab.getAttribute('aria-controls'));
  expect(panel).not.toBeNull();
  expect(panel.getAttribute('role')).toBe('tabpanel');
  expect(panel.parentElement).toBe(strip);
  expect(tab.hasAttribute('aria-selected')).toBe(true);
 }
 expect(tabs.map((x)=>x.getAttribute('aria-selected'))).toEqual(['true','false','false','false']);
 tabs[0].dispatchEvent(new w.KeyboardEvent('keydown',{key:'ArrowRight',bubbles:true,cancelable:true}));
 expect(tabs.map((x)=>x.getAttribute('aria-selected'))).toEqual(['false','true','false','false']);
});

t('a tab loads its output when it is first selected',async()=>{
 const {w,d,calls}=await start('done',ALL);
 const raw=[...d.querySelectorAll('#task-details-content [role="tab"]')].find((x)=>x.textContent.trim()==='Raw');
 raw.click();
 await sleep(40);
 expect(d.querySelector('#task-raw-content pre')).not.toBeNull();
 expect(raw.getAttribute('aria-selected')).toBe('true');
 const debug=[...d.querySelectorAll('#task-details-content [role="tab"]')].find((x)=>x.textContent.trim()==='Debug');
 debug.click();
 await sleep(40);
 expect(calls.some((c)=>c.includes('/b/tasks/42/output?type=debug'))).toBe(true);
});

t('the selected tab is drawn apart from the bar behind it',()=>{
 const bar=bodies('blacksmith.css','.manifest-tabs-nav');
 const active=bodies('blacksmith.css','.manifest-tab-btn.active');
 const bg=(b)=>(b.match(/(?:^|[;\s])background:\s*([^;]+)/)||[])[1].trim();
 expect(bg(active)).not.toBe(bg(bar));
 const pill=bodies('blacksmith.css','#task-details-modal .task-tabs-nav');
 expect(pill).not.toMatch(/border-radius:\s*9999px/);
 expect(pill).not.toMatch(/height:\s*48px/);
});

t('the metadata is one compact row table with small bold headers and tight padding',()=>{
 expect(bodies('inspection-layout.css','#task-details-modal .task-summary-table th')).toMatch(/font-size:\s*\.75rem/);
 expect(bodies('inspection-layout.css','#task-details-modal .task-summary-table th')).toMatch(/font-weight:\s*(600|700)/);
 expect(bodies('inspection-layout.css','#task-details-modal .task-summary-table td')).toMatch(/padding:\s*\.25rem/);
 expect(bodies('inspection-layout.css','#task-details-modal .task-summary-section')).not.toMatch(/border-bottom/);
 expect(bodies('inspection-layout.css','#task-details-modal .task-summary-section')).toMatch(/margin:\s*8px 0 4px/);
 const css=require('fs').readFileSync('ui/inspection-layout.css','utf8');
 expect(css).toMatch(/task-summary-table td:not\(\.task-meta-description\)\s*\{\s*white-space:\s*nowrap/);
 expect(css).toMatch(/@media \(max-width: 760px\)[\s\S]*td::before\s*\{\s*content:\s*attr\(data-label\)/);
 expect(css).not.toContain('task-summary-facts');
});

t('the title bar holds the title, the deployment, and the close button, in that order',async()=>{
 const {d}=await start('done',ALL);
 const header=d.querySelector('#task-details-modal .modal-header');
 const kids=[...header.children].map((e)=>e.id||e.className);
 expect(kids.indexOf('task-details-modal-title')).toBeLessThan(kids.indexOf('task-deployment'));
 expect(kids.indexOf('task-deployment')).toBeLessThan(kids.indexOf('modal-close'));
 const group=d.getElementById('task-deployment');
 expect(group.getAttribute('role')).toBe('group');
 expect(group.getAttribute('aria-label')).toBe('Deployment');
 expect(group.querySelector('dt, th, label')).toBeNull();
 expect(d.querySelector('#task-details-content .task-summary-facts')).toBeNull();
});

t('a long deployment name is cut by CSS and keeps its full name in title and in the copy value',async()=>{
 const LONG='valkey-standalone-ab009e66-d788-4139-a35a-2e344d987769';
 const env=boot({noDialogs:true,routes:[
  ['/b/tasks/44/output',(u,o,j)=>j(200,{output:''})],
  ['/b/tasks/44',(u,o,j)=>j(200,{task:{id:44,state:'done',description:'x',user:'admin',deployment:LONG}})],
  ['/b/status',(u,o,j)=>j(200,{env:'test',deployment:'blacksmith',instances:{},plans:{}})],
  ['/b/config/ssh/ui-terminal-status',(u,o,j)=>j(200,{})],['/v2/catalog',(u,o,j)=>j(200,{services:[]})]]});
 envs.push(env);env.w.eval(OAT);await sleep(60);
 await env.w.showTaskDetails(44);await sleep(60);
 const name=env.d.getElementById('task-deployment-text');
 expect(name.textContent).toBe(LONG);
 expect(name.title).toBe(LONG);
 expect(env.d.getElementById('task-deployment-copy').dataset.copyValue).toBe(LONG);
 const rule=bodies('inspection-layout.css','#task-details-modal .task-title-deployment .task-deployment-name');
 expect(rule).toMatch(/text-overflow:\s*ellipsis/);
 expect(rule).toMatch(/white-space:\s*nowrap/);
 expect(rule).toMatch(/overflow:\s*hidden/);
});

t('event times use the shared timestamp format, with the ISO value in the title',async()=>{
 const {w,d}=await start('done',ALL);
 const cells=[...d.querySelectorAll('#task-task-content td.timestamp')];
 expect(cells.length).toBe(4);
 const text=(secs)=>new w.Date(secs*1000).toISOString().replace('T',' ').replace(/\.\d+Z$/,' UTC');
 expect(cells[0].textContent.trim()).toBe(text(T0));
 expect(cells[3].textContent.trim()).toBe(text(T0+3));
 expect(cells[0].querySelector('time').getAttribute('title')).toBe(new w.Date(T0*1000).toISOString());
});

t('each event level is a status badge of the matching kind',async()=>{
 const {d}=await start('done',ALL);
 const badges=[...d.querySelectorAll('#task-task-content td.level .task-event-level')];
 expect(badges.map((x)=>x.textContent.trim())).toEqual(['Started','In progress','Finished','Failed']);
 expect(badges.map((x)=>x.classList.contains('status-badge'))).toEqual([true,true,true,true]);
 expect(badges.map((x)=>x.dataset.status)).toEqual(['active','active','success','error']);
});

t('a task that failed, was cancelled, or timed out summarizes where it stopped',async()=>{
 for(const [state,variant] of [['error','danger'],['timeout','danger'],['cancelled','warning']]){
  const {d}=await start(state,ALL);
  const summary=d.querySelector('#task-task-content .task-end-summary');
  expect(summary).not.toBeNull();
  expect(summary.getAttribute('role')).toBe('status');
  expect(summary.getAttribute('data-variant')).toBe(variant);
  expect(summary.textContent).toContain('Preparing deployment');
  expect(summary.textContent).toContain('boom');
  expect(summary.compareDocumentPosition(d.querySelector('#task-task-content table'))&4).toBe(4);
 }
});

t('an end summary shows the stage once when the last event has no error',async()=>{
 const {d}=await start('error',[line(),line({time:T0+1,stage:'Compiling packages',task:'ruby',state:'failed'})]);
 const text=d.querySelector('#task-task-content .task-end-summary').textContent;
 expect(text.split('Compiling packages').length-1).toBe(1);
 expect(text).toContain('ruby');
});

t('a finished task shows no summary',async()=>{
 const {d}=await start('done',ALL);
 expect(d.querySelector('#task-details-content .task-end-summary')).toBeNull();
});

t('the Deployment value has a front copy icon that copies the name and hides without a deployment',async()=>{
 const {w,d}=await start('done',ALL);
 const copied=[];
 w.navigator.clipboard={writeText:(x)=>{copied.push(x);return Promise.resolve();}};
 const dd=d.getElementById('task-deployment');
 const button=dd.querySelector('button.copy-btn-inline');
 expect(dd.firstElementChild).toBe(button);
 expect(button.getAttribute('aria-label')).toBe('Copy deployment name');
 expect(button.dataset.keepKey).toBe('task-deployment-copy');
 expect(button.querySelector('svg')).not.toBeNull();
 expect(button.hidden).toBe(false);
 expect(dd.textContent.trim()).toBe('d');
 button.click();await sleep(10);
 expect(copied).toEqual(['d']);
 expect(button.classList.contains('copied')).toBe(true);
});

t('the Deployment copy icon is hidden when the task has no deployment',async()=>{
 const env=boot({noDialogs:true,routes:[
  ['/b/tasks/43/output',(u,o,j)=>j(200,{output:''})],
  ['/b/tasks/43',(u,o,j)=>j(200,{task:{id:43,state:'done',description:'x',user:'admin'}})],
  ['/b/status',(u,o,j)=>j(200,{env:'test',deployment:'blacksmith',instances:{},plans:{}})],
  ['/b/config/ssh/ui-terminal-status',(u,o,j)=>j(200,{})],['/v2/catalog',(u,o,j)=>j(200,{services:[]})]]});
 envs.push(env);env.w.eval(OAT);await sleep(60);
 await env.w.showTaskDetails(43);await sleep(60);
 const dd=env.d.getElementById('task-deployment');
 expect(dd.querySelector('button').hidden).toBe(true);
 expect(dd.textContent.trim()).toBe('');
 expect(env.d.getElementById('task-deployment-text').hasAttribute('title')).toBe(false);
 expect(env.d.querySelector('#task-details-modal .modal-header').getBoundingClientRect).toBeDefined();
});

t('the field labels are heavier and use the foreground token',()=>{
 const body=bodies('inspection-layout.css','#task-details-modal .task-summary-table th');
 expect(body).toMatch(/font-weight:\s*(600|700)/);
 expect(body).toMatch(/color:\s*var\(--foreground\)/);
 expect(body).not.toMatch(/muted-foreground/);
});
