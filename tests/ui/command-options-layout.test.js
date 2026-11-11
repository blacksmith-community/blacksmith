const {test,expect,afterEach}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const {expectNoAlerts}=require('./certificates/no-alerts.js');
const t=JSDOM?test:test.skip;

const AVAILABILITY_URL='/b/upgrade/commands/availability';
const envs=[];
afterEach(()=>{ while(envs.length) expectNoAlerts(envs.pop()); });
async function setup(availability=[200,{enabled:true,allow_root:true}]){
 const env=boot({noDialogs:true}),{w}=env;envs.push(env);
 w.__routes.push(['/b/bosh/stemcells',(u,o,j)=>j(200,[])],['/b/bosh/releases',(u,o,j)=>j(200,[])],['/b/bosh/deployment-releases',(u,o,j)=>j(200,{})],['/b/upgrade/settings',(u,o,j)=>j(200,{})],['/b/upgrade/tasks',(u,o,j)=>j(200,[])]);
 w.__routes.push([AVAILABILITY_URL,(u,o,j)=>j(availability[0],availability[1])]);
 await sleep(30);w.serviceInstances={cache:{service_id:'valkey',plan_id:'small'}};
 await w.__T.initUpgradeTab();await sleep(10);
 await w.__T.requestOperation('command');w.__T.setBatchStep(1,false);
 return env;
}
const type=(w,el,value)=>{el.value=value;el.dispatchEvent(new w.Event('input',{bubbles:true}));};

t('max in flight is a 1, 2, 3 choice that shows only while parallel is checked and starts at 2',async()=>{
 const env=await setup();const {w,d}=env;try{
 const row=d.getElementById('command-parallel-limit-row');
 const group=d.getElementById('command-max-in-flight');
 expect(row.hidden).toBe(true);
 d.getElementById('command-parallel').click();
 expect(row.hidden).toBe(false);
 expect(group.tagName).toBe('FIELDSET');
 expect(group.querySelector('legend').textContent).toBe('Max in flight');
 const radios=[...group.querySelectorAll('input[type=radio]')];
 expect(radios.map(r=>r.value)).toEqual(['1','2','3']);
 expect(radios.map(r=>r.checked)).toEqual([false,true,false]);
 expect(new Set(radios.map(r=>r.name)).size).toBe(1);
 expect(radios.every(r=>!r.disabled&&r.tabIndex>=0)).toBe(true);
 expect(group.textContent).not.toContain('All');
 d.getElementById('command-parallel').click();
 expect(row.hidden).toBe(true);
 }finally{w.close();}
});

t('choosing a max in flight value updates the state and keeps the same control',async()=>{
 const env=await setup();const {w,d}=env;try{
 d.getElementById('command-parallel').click();
 const group=d.getElementById('command-max-in-flight');
 const three=group.querySelector('input[value="3"]');
 three.focus();three.click();
 expect(w.__T.commandState.maxInFlight).toBe('3');
 expect(d.getElementById('command-max-in-flight')).toBe(group);
 expect(group.contains(d.activeElement)).toBe(true);
 expect(d.activeElement.value).toBe('3');
 const css=fs.readFileSync(path.join(__dirname,'..','..','ui','forge-theme.css'),'utf8');
 const rule=(css.match(/#upgrade \.command-segment input[^{}]*\{[^}]*\}/)||[''])[0];
 expect(rule).not.toContain('display: none');expect(rule).not.toContain('visibility: hidden');
 expect(css).toMatch(/\.command-segment:has\(input:focus-visible\)/);
 expect(css).toMatch(/\.command-segment:has\(input:checked\)/);
 }finally{w.close();}
});

t('Next is enabled with the defaults once a command is typed',async()=>{
 const env=await setup();const {w,d}=env;try{
 const next=d.getElementById('batch-flow-next');
 d.getElementById('command-parallel').click();
 expect(w.__T.batchOptionIssue()).toBe('Enter the command to run.');
 type(w,d.getElementById('command-text'),'uptime');
 expect(w.__T.batchOptionIssue()).toBe('');
 expect(next.disabled).toBe(false);
 }finally{w.close();}
});

t('a value above the director limit shows its message beside the choice, and only while parallel is on',async()=>{
 const env=await setup();const {w,d}=env;try{
 type(w,d.getElementById('command-text'),'uptime');
 const msg=d.getElementById('command-max-in-flight-error');
 const group=d.getElementById('command-max-in-flight');
 expect(msg.hidden).toBe(true);
 w.__T.recordCommandPreview({parallel_available:true,worker_count:2,max_in_flight_limit:1});
 expect(msg.hidden).toBe(true);
 d.getElementById('command-parallel').click();
 expect(msg.hidden).toBe(false);
 expect(msg.textContent).toBe('The director allows at most 1 node at once. Choose a lower max in flight.');
 expect(group.getAttribute('aria-describedby')).toContain('command-max-in-flight-error');
 expect(w.__T.batchOptionIssue()).toBe(msg.textContent);
 group.querySelector('input[value="1"]').click();
 expect(msg.hidden).toBe(true);
 group.querySelector('input[value="2"]').click();
 expect(msg.hidden).toBe(false);
 d.getElementById('command-parallel').click();
 expect(msg.hidden).toBe(true);
 }finally{w.close();}
});

t('the parallel note follows the checkbox and the limit',async()=>{
 const env=await setup();const {w,d}=env;try{
 const note=d.getElementById('command-parallel-note');
 expect(note.textContent).toBe('Nodes run one after another, and a failure stops the run early.');
 expect(note.textContent).not.toContain('off by default');
 d.getElementById('command-parallel').click();
 expect(note.textContent).toBe('Up to 2 nodes run at once.');
 d.querySelector('#command-max-in-flight input[value="3"]').click();
 expect(note.textContent).toBe('Up to 3 nodes run at once.');
 d.querySelector('#command-max-in-flight input[value="1"]').click();
 expect(note.textContent).toBe('Up to 1 node runs at once.');
 d.getElementById('command-parallel').click();
 expect(note.textContent).toBe('Nodes run one after another, and a failure stops the run early.');
 }finally{w.close();}
});

t('root is disabled, noted, and styled as off when the broker switches it off',async()=>{
 const env=await setup([200,{enabled:true,allow_root:false}]);const {w,d}=env;try{
 const root=d.getElementById('command-user-root');
 expect(root.disabled).toBe(true);
 expect(d.getElementById('command-root-note').textContent).toBe('Running as root is switched off on this broker.');
 const css=fs.readFileSync(path.join(__dirname,'..','..','ui','forge-theme.css'),'utf8');
 expect(/command-user-option[^{]*:disabled[^{]*\{[^}]*(opacity|cursor)/.test(css)||/command-user-option:has\(input:disabled\)[^{]*\{[^}]*(opacity|cursor)/.test(css)).toBe(true);
 }finally{w.close();}
});

t('the pane has no Shell line',async()=>{
 const env=await setup();const {d}=env;
 expect(d.getElementById('batch-flow-options').textContent).not.toContain('Shell:');
});

t('the command box is eight rows of monospace text that resizes vertically',async()=>{
 const env=await setup();const {d}=env;
 const text=d.getElementById('command-text');
 expect(text.rows).toBe(8);
 const css=fs.readFileSync(path.join(__dirname,'..','..','ui','forge-theme.css'),'utf8');
 const rule=(css.match(/#upgrade[^{}]*textarea\.command-text[^{}]*\{[^}]*\}/)||[''])[0];
 expect(rule).toContain('font-family');expect(rule).toContain('resize: vertical');expect(rule).toContain('width: 100%');
 // The box spans the pane, so neither the pane nor its row may cap it at the reading column.
 const cap=(sel)=>{const m=css.match(new RegExp(sel.replace(/[.#]/g,'\\$&')+' \\{[^}]*\\}'));return m?m[0]:'';};
 expect(cap('#upgrade .batch-flow-options .command-pane')).not.toMatch(/max-width: \d/);
 expect(cap('#upgrade .command-pane .filter-row.command-text-row')).toMatch(/max-width: none/);
 expect(cap('#upgrade .command-pane .command-text-row .batch-field-help')).toMatch(/max-width: 640px/);
 expect(d.getElementById('command-text').closest('.filter-row').classList.contains('command-text-row')).toBe(true);
});

t('each field puts its label first, then its control, then its help',async()=>{
 const env=await setup();const {d}=env;
 d.getElementById('command-parallel').click();
 const pane=d.querySelector('#batch-flow-options .command-pane');
 const before=(a,b)=>Boolean(a.compareDocumentPosition(b)&4);
 [['command-text','command-text-help'],['command-timeout','command-timeout-help'],['command-max-in-flight','command-max-in-flight-help'],['command-parallel','command-parallel-note']].forEach(([control,help])=>{
  const c=pane.querySelector('#'+control),h=pane.querySelector('#'+help);
  expect(before(c.tagName==='FIELDSET'?c.querySelector('legend'):c,h)).toBe(true);
  const label=c.tagName==='INPUT'&&c.type==='checkbox'?c.closest('label'):c.tagName==='FIELDSET'?c.querySelector('legend'):pane.querySelector('label[for="'+control+'"]');
  if(c.tagName!=='FIELDSET'&&label!==c.closest('label')) expect(before(label,c)).toBe(true);
  expect(c.getAttribute('aria-describedby')).toContain(help);
 });
 const css=fs.readFileSync(path.join(__dirname,'..','..','ui','forge-theme.css'),'utf8');
 const row=(css.match(/#upgrade \.command-pane \.filter-row \{[^}]*\}/)||[''])[0];
 expect(row).not.toContain('nowrap');
 expect(row).toMatch(/display: grid|flex-direction: column/);
});

t('the pane leaves room below the last field for the sticky footer',()=>{
 const css=fs.readFileSync(path.join(__dirname,'..','..','ui','forge-theme.css'),'utf8');
 expect(/#upgrade \.batch-flow-options \.command-pane \{[^}]*padding-bottom: (1[0-9][0-9]|[2-9][0-9][0-9])px/.test(css)).toBe(true);
});
