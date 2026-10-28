const {test,expect,afterEach}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const {expectNoAlerts}=require('./certificates/no-alerts.js');
const t=JSDOM?test:test.skip;

const SECRET='echo SECRET-COMMAND-TEXT-9141';
const PREVIEW_URL='/b/upgrade/commands/preview';

// probes maps the run_as a probe sends to the answer it gets, as [status, body].
// Every env a test boots, checked once the test ends so a dialog error that
// the UI code swallows still fails it.
const envs=[];
afterEach(()=>{ while(envs.length){ const env=envs.pop(); expectNoAlerts(env); expect(env.logs.some(l=>String(l).includes(SECRET))).toBe(false); } });
async function setup(probes={},{network=false}={}){
 const env=boot({noDialogs:true}),{w}=env;envs.push(env);
 w.__routes.push(['/b/bosh/stemcells',(u,o,j)=>j(200,[])],['/b/bosh/releases',(u,o,j)=>j(200,[])],['/b/bosh/deployment-releases',(u,o,j)=>j(200,{})],['/b/upgrade/settings',(u,o,j)=>j(200,{})],['/b/upgrade/tasks',(u,o,j)=>j(200,[])]);
 w.__routes.push([PREVIEW_URL,(u,o,j)=>{
  if(network) return Promise.reject(new Error('network down'));
  const answer=probes[JSON.parse(o.body).run_as];
  return answer?j(answer[0],answer[1]):j(400,{success:false,error:'Select at least one instance to preview.',code:'no_selections'});
 }]);
 await sleep(30);w.serviceInstances={cache:{service_id:'valkey',plan_id:'small'}};
 await w.__T.initUpgradeTab();await sleep(10);
 return env;
}
async function openOptions(env){
 await env.w.__T.requestOperation('command');env.w.__T.setBatchStep(1,false);
}
const type=(w,el,value)=>{el.value=value;el.dispatchEvent(new w.Event('input',{bubbles:true}));};
const refusal=(code,message)=>[403,{success:false,error:message,code}];

t('the operation selector offers a Run command radio with its hint',async()=>{
 const env=await setup();const {w,d}=env;try{
 const radio=d.getElementById('rotate-op-command');
 expect(radio.type).toBe('radio');expect(radio.value).toBe('command');expect(radio.disabled).toBe(false);
 expect(radio.closest('label').querySelector('strong').textContent).toBe('Run command');
 expect(radio.closest('label').querySelector('.batch-operation-description').textContent).toContain('shell command');
 expectNoAlerts(env);
 }finally{w.close();}
});

t('the options pane holds the command fields with their defaults',async()=>{
 const env=await setup();const {w,d}=env;try{
 await openOptions(env);
 const options=d.getElementById('batch-flow-options');
 expect(options.hidden).toBe(false);
 const text=options.querySelector('#command-text');
 expect(text.tagName).toBe('TEXTAREA');expect(text.rows).toBe(6);expect(text.getAttribute('spellcheck')).toBe('false');expect(text.getAttribute('autocomplete')).toBe('off');
 expect(options.querySelector('#command-user-vcap').checked).toBe(true);
 expect(options.querySelector('#command-user-root').checked).toBe(false);
 expect(options.querySelector('#command-user-root').disabled).toBe(false);
 expect(options.textContent).toContain('Shell: /bin/bash (fixed in this version)');
 expect(options.querySelector('#command-timeout').value).toBe('600');
 expect(options.querySelector('#command-timeout').min).toBe('10');expect(options.querySelector('#command-timeout').max).toBe('3600');
 expect(options.querySelector('#command-parallel').checked).toBe(false);
 const limit=options.querySelector('#command-max-in-flight');
 expect(limit.closest('[hidden]')).not.toBeNull();
 expect(limit.min).toBe('1');expect(limit.hasAttribute('max')).toBe(false);
 expect(options.querySelector('#rotate-name')).not.toBeNull();expect(options.querySelector('#rotate-reason')).not.toBeNull();
 options.querySelector('#command-parallel').click();
 expect(limit.closest('[hidden]')).toBeNull();
 expectNoAlerts(env);
 }finally{w.close();}
});

t('the probes send no command text and each user is probed',async()=>{
 const env=await setup();const {w}=env;try{
 const probes=env.calls.filter(c=>c.startsWith('POST '+PREVIEW_URL));
 expect(probes.length).toBe(2);
 const bodies=probes.map(c=>JSON.parse(c.slice(('POST '+PREVIEW_URL+' ').length)));
 expect(bodies.map(b=>b.run_as).sort()).toEqual(['root','vcap']);
 bodies.forEach(b=>{expect(b.instance_ids).toEqual([]);expect((b.command||'').length).toBe(0);});
 }finally{w.close();}
});

t('a root_disabled probe disables root with a note',async()=>{
 const env=await setup({root:refusal('root_disabled','Running as root is switched off.')});const {w,d}=env;try{
 expect(d.getElementById('rotate-op-command').disabled).toBe(false);
 await openOptions(env);
 const root=d.getElementById('command-user-root');
 expect(root.disabled).toBe(true);
 expect(d.getElementById(root.getAttribute('aria-describedby')).textContent).toBe('Running as root is switched off on this broker.');
 expect(d.getElementById('command-user-vcap').checked).toBe(true);
 }finally{w.close();}
});

t('a later preview refusing root disables root too',async()=>{
 const env=await setup();const {w,d}=env;try{
 await openOptions(env);
 d.getElementById('command-user-root').click();
 expect(d.getElementById('command-user-root').checked).toBe(true);
 w.__T.recordCommandRefusal({status:403,code:'root_disabled',message:'Root is off now.'});
 expect(d.getElementById('command-user-root').disabled).toBe(true);
 expect(d.getElementById('command-user-vcap').checked).toBe(true);
 }finally{w.close();}
});

t('a commands_disabled probe disables the radio with the reason',async()=>{
 const env=await setup({vcap:refusal('commands_disabled','Command runs are switched off on this broker.')});const {w,d}=env;try{
 const radio=d.getElementById('rotate-op-command');
 expect(radio.disabled).toBe(true);
 expect(d.getElementById(radio.getAttribute('aria-describedby')).textContent).toContain('Command runs are switched off on this broker.');
 expect(await w.__T.requestOperation('command')).toBe(false);
 expect(d.getElementById('command-text')).toBeNull();
 }finally{w.close();}
});

t('a commands_disabled answer to the root probe also disables the radio',async()=>{
 const env=await setup({root:refusal('commands_disabled','Off.')});const {w,d}=env;try{
 expect(d.getElementById('rotate-op-command').disabled).toBe(true);
 }finally{w.close();}
});

t('other probe answers leave the controls enabled',async()=>{
 const env=await setup({vcap:[401,{success:false,error:'Command key needed.',code:'command_credential_required'}]});const {w,d}=env;try{
 expect(d.getElementById('rotate-op-command').disabled).toBe(false);
 await openOptions(env);
 expect(d.getElementById('command-user-root').disabled).toBe(false);
 }finally{w.close();}
});

t('the option issue follows the command, timeout, and parallel fields',async()=>{
 const env=await setup();const {w,d}=env;try{
 await openOptions(env);
 const next=d.getElementById('batch-flow-next');
 const issue=()=>w.__T.batchOptionIssue();
 expect(issue()).toBeTruthy();expect(next.disabled).toBe(true);
 type(w,d.getElementById('command-text'),'   \n ');expect(issue()).toBeTruthy();
 type(w,d.getElementById('command-text'),SECRET);expect(issue()).toBe('');expect(next.disabled).toBe(false);
 const timeout=d.getElementById('command-timeout');
 ['9','3601','','abc','10.5'].forEach(v=>{type(w,timeout,v);expect(issue()).toBeTruthy();expect(next.disabled).toBe(true);});
 ['10','3600','600'].forEach(v=>{type(w,timeout,v);expect(issue()).toBe('');});
 d.getElementById('command-parallel').click();
 const limit=d.getElementById('command-max-in-flight');
 ['','0','-1','x','1.5'].forEach(v=>{type(w,limit,v);expect(issue()).toBeTruthy();expect(next.disabled).toBe(true);});
 type(w,limit,'250');expect(issue()).toBe('');expect(next.disabled).toBe(false);
 d.getElementById('command-parallel').click();type(w,limit,'');expect(issue()).toBe('');
 expect(issue().includes(SECRET)).toBe(false);
 }finally{w.close();}
});

t('a preview bounds max in flight by its limit',async()=>{
 const env=await setup();const {w,d}=env;try{
 await openOptions(env);
 type(w,d.getElementById('command-text'),SECRET);
 d.getElementById('command-parallel').click();
 const limit=d.getElementById('command-max-in-flight');
 type(w,limit,'4');expect(w.__T.batchOptionIssue()).toBe('');
 w.__T.recordCommandPreview({parallel_available:true,worker_count:4,max_in_flight_limit:3});
 expect(limit.max).toBe('3');expect(w.__T.batchOptionIssue()).toBeTruthy();
 type(w,limit,'3');expect(w.__T.batchOptionIssue()).toBe('');
 }finally{w.close();}
});

t('a preview without parallel capacity disables the checkbox with a note',async()=>{
 const env=await setup();const {w,d}=env;try{
 await openOptions(env);
 type(w,d.getElementById('command-text'),SECRET);
 d.getElementById('command-parallel').click();
 w.__T.recordCommandPreview({parallel_available:false,worker_count:1,max_in_flight_limit:0});
 const box=d.getElementById('command-parallel');
 expect(box.disabled).toBe(true);expect(box.checked).toBe(false);
 expect(d.getElementById(box.getAttribute('aria-describedby')).textContent).toContain('too few workers');
 expect(w.__T.batchOptionIssue()).toBe('');
 }finally{w.close();}
});

t('the command text stays out of the page, requests, and storage',async()=>{
 const env=await setup();const {w,d}=env;try{
 await openOptions(env);
 type(w,d.getElementById('command-text'),SECRET);
 d.getElementById('command-parallel').click();
 w.__T.recordCommandPreview({parallel_available:true,worker_count:4,max_in_flight_limit:3});
 w.__T.setBatchStep(0,false);w.__T.setBatchStep(1,false);
 const outside=d.documentElement.cloneNode(true);
 outside.querySelector('#command-text').value='';outside.querySelector('#command-text').textContent='';
 expect(outside.outerHTML.includes(SECRET)).toBe(false);
 expect(env.calls.some(c=>c.includes(SECRET))).toBe(false);
 expect(env.errors.some(e=>e.includes(SECRET))).toBe(false);
 expect(JSON.stringify(Object.entries(w.localStorage)).includes(SECRET)).toBe(false);
 expect(d.getElementById('command-text').value===SECRET).toBe(true);
 expectNoAlerts(env);
 }finally{w.close();}
});

t('leaving the operation hides the command pane and returns the upgrade options',async()=>{
 const env=await setup();const {w,d}=env;try{
 await openOptions(env);
 await w.__T.requestOperation('upgrade');w.__T.setBatchStep(1,false);
 expect(d.getElementById('command-text')).toBeNull();
 expect(d.getElementById('batch-flow-options').hidden).toBe(false);
 }finally{w.close();}
});

t('a probe that fails on the network leaves the controls enabled',async()=>{
 const env=await setup({},{network:true});const {w,d}=env;try{
 expect(d.getElementById('rotate-op-command').disabled).toBe(false);
 await openOptions(env);
 expect(d.getElementById('command-user-root').disabled).toBe(false);
 }finally{w.close();}
});

t('a preview limit of 0 with parallel available sets no upper bound',async()=>{
 const env=await setup();const {w,d}=env;try{
 await openOptions(env);
 type(w,d.getElementById('command-text'),SECRET);
 d.getElementById('command-parallel').click();
 w.__T.recordCommandPreview({parallel_available:true,worker_count:0,max_in_flight_limit:0});
 const limit=d.getElementById('command-max-in-flight');
 expect(limit.hasAttribute('max')).toBe(false);expect(d.getElementById('command-parallel').disabled).toBe(false);
 type(w,limit,'500');expect(w.__T.batchOptionIssue()).toBe('');
 }finally{w.close();}
});

t('a command run on a leftover selection can leave the instances step',async()=>{
 const env=await setup();const {w,d}=env;try{
 w.__T.selectedUpgradeInstances.add('cache');
 await openOptions(env);
 type(w,d.getElementById('command-text'),SECRET);
 const next=d.getElementById('batch-flow-next');
 expect(next.disabled).toBe(false);next.click();
 expect(w.__T.batchFlow.step).toBe(2);
 expect(w.__T.batchOptionIssue()).toBe('');
 expect(next.disabled).toBe(false);
 }finally{w.close();}
});

t('the command pane shows placeholders for commands, not certificates',async()=>{
 const env=await setup();const {w,d}=env;try{
 await openOptions(env);
 expect(d.getElementById('rotate-name').placeholder).not.toContain('certificate');
 expect(d.getElementById('rotate-reason').placeholder).not.toContain('renewal');
 await w.__T.requestOperation('rotate');
 expect(d.getElementById('rotate-name').placeholder).toBe('For example, October certificate renewal');
 }finally{w.close();}
});

// Oat lays a radio's label out inline, and the two Run as labels are built with no whitespace between them,
// so without room of their own the first label's text runs into the second radio. The layout itself needs a
// real browser. This pins the room the stylesheet gives each option.
t('the Run as options keep room between them',async()=>{
 const env=await setup();const {w,d}=env;try{
 await openOptions(env);
 const options=[...d.querySelectorAll('#batch-flow-options fieldset.command-user > label.command-user-option')];
 expect(options.map(o=>o.querySelector('input').value)).toEqual(['vcap','root']);
 expect(options[0].nextSibling).toBe(options[1]);
 const css=require('fs').readFileSync(require('path').join(__dirname,'../../ui/forge-theme.css'),'utf8');
 const rule=css.match(/(?:^|\n)#upgrade \.command-user-option\s*\{([^}]*)\}/);
 expect(rule!==null).toBe(true);
 const room=rule[1].match(/margin-inline-end:\s*(\d+)px/);
 expect(room!==null).toBe(true);
 expect(Number(room[1])).toBeGreaterThanOrEqual(16);
 expect(rule[1]).not.toMatch(/(^|[;{\s])(border|border-color|border-radius|background|background-color|color|box-shadow|outline|transition):/);
 expectNoAlerts(env);
 }finally{w.close();}
});
