const {test,expect,afterEach}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const UI=path.resolve(__dirname,'..','..','ui');
const OAT=fs.readFileSync(path.join(UI,'vendor','oat','oat.min.js'),'utf8');
const KEY='blacksmith.terminal.sessions';
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
afterEach(()=>{ while(envs.length){ const env=envs.pop(); try{ expect(env.alerts).toEqual([]); expect(env.errors).toEqual([]); } finally { env.w.close(); } } });
const record=(ids,active)=>JSON.stringify({v:1,active,sessions:ids.map(i=>({deploymentType:'blacksmith',deploymentName:'broker',instanceId:i,instanceName:'node',boshDeploymentName:'broker'}))});
const start=async(preset)=>{
 const env=boot({noDialogs:true,routes:[['/b/status',(u,o,j)=>j(200,{env:'test',deployment:'blacksmith',instances:{},plans:{}})],['/b/config/ssh/ui-terminal-status',(u,o,j)=>j(200,{})],['/v2/catalog',(u,o,j)=>j(200,{services:[]})]]});envs.push(env);
 const {w}=env;
 if(preset!==undefined) w.sessionStorage.setItem(KEY,preset);
 w.WTermLib={WTerm:class{constructor(el){this.cols=80;this.rows=24;el.classList.add('wterm');}init(){return Promise.resolve(this);}focus(){}write(){}setRenderingPaused(){}destroy(){}},version:'0.5.10'};
 w.WebSocket=class{static get OPEN(){return 1;}constructor(){this.readyState=0;}send(){}close(){this.readyState=3;}};
 w.eval(OAT);
 await sleep(80);
 return {...env,mgr:w.TerminalManager,d:w.document};
};

t('the terminal dock is the last row of the page, a labelled region',()=>{
 const html=fs.readFileSync(path.join(UI,'index.html'),'utf8');
 const dock=html.indexOf('id="minimized-terminal-bar"');
 expect(dock).toBeGreaterThan(html.indexOf('</main>'));
 const tag=html.slice(html.lastIndexOf('<',dock),html.indexOf('>',dock)+1);
 expect(tag).toMatch(/role="region"/);
 expect(tag).toMatch(/aria-label="Terminal sessions"/);
});

t('the dock sits in the page flow, so it reserves its own height',()=>{
 const bar=bodies('blacksmith.css','.minimized-terminal-bar');
 expect(bar).not.toMatch(/position:\s*(fixed|absolute)/);
 expect(bar).toMatch(/flex:\s*none/);
 expect(bar).toMatch(/border-top:/);
 expect(bodies('blacksmith.css','.minimized-terminal-bar.active')).toMatch(/display:\s*flex/);
 expect(bodies('console.css','#minimized-terminal-bar')).not.toMatch(/top:/);
 expect(bodies('blacksmith.css','body.terminal-minimized .tabs')).toBe('');
});

t('the dock stays below drawers, dialogs, and toasts',()=>{
 expect(bodies('blacksmith.css','.minimized-terminal-bar')).toMatch(/z-index:\s*var\(--z-terminal-dock\)/);
 const z=Number((bodies('blacksmith.css',':root').match(/--z-terminal-dock:\s*(\d+)/)||[])[1]);
 expect(z).toBeLessThanOrEqual(5);
});

t('the dock row scrolls sideways inside itself',()=>{
 expect(bodies('blacksmith.css','.minimized-terminal-tabs')).toMatch(/overflow-x:\s*auto/);
});

t('docked tabs get a Terminals button that opens a list on a phone, with a count',async()=>{
 const env=await start(record(['node/0','node/1','node/2'],'blacksmith-broker-node/1'));
 const {d}=env;
 const dock=d.getElementById('minimized-terminal-bar');
 expect(dock.classList.contains('active')).toBe(true);
 const toggle=d.getElementById('terminal-dock-toggle');
 expect(toggle.textContent.trim()).toBe('Terminals (3)');
 expect(toggle.getAttribute('aria-expanded')).toBe('false');
 expect(toggle.getAttribute('aria-controls')).toBe('minimized-terminal-tabs');
 expect(toggle.getAttribute('data-keep-key')).toBe('terminal-dock:toggle');
 toggle.click();
 expect(toggle.getAttribute('aria-expanded')).toBe('true');
 expect(dock.classList.contains('dock-open')).toBe(true);
 toggle.click();
 expect(dock.classList.contains('dock-open')).toBe(false);
 // Restoring an entry shuts the list and opens the dialog as before.
 toggle.click();
 d.querySelectorAll('#minimized-terminal-tabs .minimized-session-open')[2].click();
 expect(dock.classList.contains('dock-open')).toBe(false);
 expect(dock.classList.contains('active')).toBe(false);
 expect(env.mgr.activeSession).toBe('blacksmith-broker-node/2');
});

t('the count follows closed sessions and every dock control is labelled and keyed',async()=>{
 const env=await start(record(['node/0','node/1'],'blacksmith-broker-node/0'));
 const {d}=env;
 const dock=d.getElementById('minimized-terminal-bar');
 for(const b of dock.querySelectorAll('button')){
  expect(b.getAttribute('aria-label')||b.textContent.trim()).toBeTruthy();
  expect(b.getAttribute('data-keep-key')).toBeTruthy();
 }
 const keys=[...dock.querySelectorAll('[data-keep-key]')].map(b=>b.getAttribute('data-keep-key'));
 expect(new Set(keys).size).toBe(keys.length);
 d.querySelectorAll('#minimized-terminal-tabs .tab-close')[0].click();
 expect(d.getElementById('terminal-dock-toggle').textContent.trim()).toBe('Terminals (1)');
});

t('the dock keeps clear of the device safe areas',()=>{
 const bar=bodies('blacksmith.css','.minimized-terminal-bar');
 expect(bar).toMatch(/padding-bottom:\s*max\(4px,\s*env\(safe-area-inset-bottom\)\)/);
 expect(bar).toMatch(/padding-left:\s*max\(16px,\s*env\(safe-area-inset-left\)\)/);
 expect(bar).toMatch(/padding-right:\s*max\(16px,\s*env\(safe-area-inset-right\)\)/);
});

t('every dock control has a 44px hit area on a coarse pointer',()=>{
 const css=strip(fs.readFileSync(path.join(UI,'blacksmith.css'),'utf8'));
 const m=css.match(/@media\s*\(pointer:\s*coarse\)\s*\{([\s\S]*?\n\})/);
 expect(m).not.toBeNull();
 const block=m[1];
 for(const sel of ['.minimized-terminal-bar .terminal-control-btn','.minimized-terminal-bar .terminal-dock-toggle','.minimized-terminal-bar .minimized-session-open','.minimized-terminal-bar .tab-close','.minimized-terminal-bar .terminal-reconnect']){
  const rule=block.split('}').find((r)=>r.includes(sel));
  expect(rule).toBeDefined();
  expect(rule).toMatch(/min-height:\s*44px/);
  expect(rule).toMatch(/min-width:\s*44px/);
 }
});
