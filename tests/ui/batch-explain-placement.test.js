const {test,expect,afterEach}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const {setupRotate,cert}=require('./rotate-flow-support.js');
const t=JSDOM?test:test.skip;

// Routes for the page's own load, so a console error means something broke.
const QUIET=[[/\/b\/status$/,(u,o,j)=>j(200,{env:'Local fixture',instances:{},plans:{}})],[/\/b\/instance$/,(u,o,j)=>j(200,{deployment:'blacksmith'})],['/v2/catalog',(u,o,j)=>j(200,{services:[]})],['/b/config/ssh/ui-terminal-status',(u,o,j)=>j(200,{})],[/inventory\?summary_only=true/,(u,o,j)=>j(200,{summary:{certificates:0,needs_attention:0}})]];
const envs=[];
afterEach(()=>{while(envs.length){const env=envs.pop();expect(env.alerts).toEqual([]);expect(env.errors).toEqual([]);env.w.close();}});
async function setupCommand(){
 const env=boot({noDialogs:true,guidance:true,routes:QUIET}),{w}=env;envs.push(env);
 w.__routes.push(['/b/bosh/stemcells',(u,o,j)=>j(200,[])],['/b/bosh/releases',(u,o,j)=>j(200,[])],['/b/bosh/deployment-releases',(u,o,j)=>j(200,{})],['/b/upgrade/settings',(u,o,j)=>j(200,{})],['/b/upgrade/tasks',(u,o,j)=>j(200,[])],
  ['/b/upgrade/commands/preview',(u,o,j)=>j(400,{success:false,error:'Select at least one instance to preview.',code:'no_selections'})]);
 await sleep(30);w.serviceInstances={cache:{service_id:'valkey',plan_id:'small'}};
 await w.__T.initUpgradeTab();await sleep(10);
 await w.__T.requestOperation('command');w.__T.setBatchStep(1,false);
 return env;
}
// The info button for key, on the same line as anchor and outside any label
// or tablist. A legend holds the button, a heading shares a flex row with it,
// and a label in a filter row has it as the next sibling.
function infoAfter(anchor,key){
 expect(anchor===null).toBe(false);
 let node;
 if(anchor.tagName==='LEGEND'){
  node=anchor.querySelector(':scope > .field-guidance');
  expect(anchor.parentElement.getAttribute('aria-labelledby')).toBe(anchor.querySelector(':scope > .field-guidance-text').id);
 }else if(/^H[1-6]$/.test(anchor.tagName)){
  expect(anchor.parentElement.classList.contains('field-guidance-row')).toBe(true);
  node=anchor.nextElementSibling;
 }else node=anchor.nextElementSibling;
 const button=node&&node.classList.contains('field-guidance')?node.querySelector('button[data-context-help]'):null;
 expect(button===null).toBe(false);
 expect(button.getAttribute('aria-controls').startsWith('batch-help-'+key)).toBe(true);
 expect(button.closest('label')===null).toBe(true);
 expect(button.closest('[role=tablist]')===null).toBe(true);
 return button;
}

t('Run command options put an info button beside each command control',async()=>{
 const env=await setupCommand();const {d}=env;
 const pane=d.querySelector('#batch-flow-options .command-pane');
 infoAfter(pane.querySelector('label[for="command-text"]'),'command-text');
 infoAfter(pane.querySelector('.command-user > legend'),'command-user');
 infoAfter(pane.querySelector('label[for="command-timeout"]'),'command-timeout');
 infoAfter(pane.querySelector('.command-parallel-option'),'command-parallel');
});

t('info buttons stay usable while a command create is pending',async()=>{
 const env=boot({noDialogs:true,guidance:true,routes:QUIET}),{w,d}=env;envs.push(env);
 const vm=index=>({group:'node',index,id:'vm-'+index,cid:'cid-'+index,job_state:'running',targeted:true});
 const preview={preview_id:'prev-1',expires_at:'2030-01-01T00:00:00Z',command_digest:'d',command_length:6,run_as:'vcap',shell:'/bin/bash',timeout_seconds:600,parallel:false,max_in_flight:0,parallel_available:true,worker_count:4,max_in_flight_limit:0,services:['valkey'],warnings:[],
  instances:[{instance_id:'val-a',instance_name:'val-a-name',deployment:'dep-val-a',service:'valkey',plan:'small',blocked:null,vms:[vm(0)]}]};
 w.__routes.push(['/b/bosh/stemcells',(u,o,j)=>j(200,[])],['/b/bosh/releases',(u,o,j)=>j(200,[])],['/b/bosh/deployment-releases',(u,o,j)=>j(200,{})],['/b/upgrade/settings',(u,o,j)=>j(200,{})],
  ['/b/upgrade/commands/preview',(u,o,j)=>JSON.parse(o.body).instance_ids.length?j(200,preview):j(400,{success:false,error:'Select at least one instance to preview.',code:'no_selections'})],
  ['/b/upgrade/tasks',(u,o,j)=>(o.method||'GET')==='POST'?j(401,{success:false,error:'A command key is required.',code:'command_credential_required'}):j(200,[])]);
 w.plansData={services:[{id:'valkey',name:'valkey'}]};
 await sleep(30);w.serviceInstances={'val-a':{service_id:'valkey',plan_id:'small',plan:{id:'small',name:'small'}}};
 await w.__T.initUpgradeTab();await sleep(10);
 await w.__T.requestOperation('command');w.__T.setBatchStep(1,false);
 const text=d.getElementById('command-text');text.value='uptime';text.dispatchEvent(new w.Event('input',{bubbles:true}));
 w.__T.setBatchStep(2,false);await sleep(5);
 d.querySelector('#upgrade .upgrade-instance-item[data-instance-id="val-a"] input[type="checkbox"]').click();
 w.__T.runBatchReview();await sleep(30);
 const ack=d.getElementById('batch-flow-ack');ack.value='run as vcap';ack.dispatchEvent(new w.Event('input',{bubbles:true}));
 d.getElementById('batch-flow-next').click();await sleep(30);
 expect(d.getElementById('command-text').disabled).toBe(true);
 const buttons=[...d.querySelectorAll('#batch-flow-options .command-pane .field-guidance button')];
 expect(buttons.length).toBe(4);
 expect(buttons.some(b=>b.disabled)).toBe(false);
});

t('Rotate options hide the Mode legend and keep its info button in the tab strip corner',async()=>{
 const env=await setupRotate({certs:[cert('/s/a_crt','a_crt',5,'inst-a','cache-a')],bootOptions:{noDialogs:true,guidance:true,routes:QUIET}});envs.push(env);
 const {d}=env;
 const mode=d.getElementById('rotate-mode');
 const legend=mode.querySelector(':scope > legend');
 expect(legend.textContent).toBe('Mode');
 expect(legend.classList.contains('visually-hidden')).toBe(true);
 expect(legend.querySelector('button')).toBe(null);
 const help=mode.querySelector(':scope > .batch-mode-help');
 const button=help.querySelector('button[data-context-help]');
 expect(button.getAttribute('aria-controls')).toBe('batch-help-rotate-mode');
 expect(button.closest('bs-tabs')).toBe(null);
 expect(button.closest('[role=tablist]')).toBe(null);
 expect(button.getAttribute('role')).toBe(null);
 const tabs=[...mode.querySelectorAll('bs-tabs [role=tab]')];
 expect(tabs.map(t=>t.textContent)).toEqual(['Redeploy','Patch in place']);
 expect(mode.querySelector('bs-tabs').tabs.length).toBe(2);
 expect(tabs.some(t=>t.contains(button))).toBe(false);
});

t('the Mode info button comes before the tab strip in the DOM',async()=>{
 const env=await setupRotate({certs:[cert('/s/a_crt','a_crt',5,'inst-a','cache-a')],bootOptions:{noDialogs:true,guidance:true,routes:QUIET}});envs.push(env);
 const {d}=env;
 const mode=d.getElementById('rotate-mode');
 const help=mode.querySelector(':scope > .batch-mode-help');
 const strip=mode.querySelector(':scope > bs-tabs');
 expect(help.nextElementSibling).toBe(strip);
 expect(Boolean(help.compareDocumentPosition(strip)&4)).toBe(true);
 const focusable=[...mode.querySelectorAll('button, [role=tab]')].map(e=>e.matches('[data-context-help]')?'help':'tab');
 expect(focusable.slice(0,3)).toEqual(['help','tab','tab']);
});

t('the Mode strip without tabs keeps a hidden legend and the same info button',async()=>{
 const env=boot({noDialogs:true,guidance:true,oat:true,routes:QUIET});envs.push(env);
 const {w}=env;await sleep(30);
 const controls=w.__T.buildModeControls({prefix:'rotate-dialog-x',name:'rotate-dialog-mode-x',state:{mode:'redeploy',converge:''},blockReason:()=>'',onChange(){}});
 const mode=controls.root;
 expect(mode.querySelector('bs-tabs')).toBe(null);
 const legend=mode.querySelector(':scope > legend');
 expect(legend.classList.contains('visually-hidden')).toBe(true);
 expect(mode.querySelectorAll('.rotate-mode-option input[type=radio]').length).toBe(2);
 const button=mode.querySelector(':scope > .batch-mode-help button[data-context-help]');
 expect(button.closest('label')).toBe(null);
 expect(button.getAttribute('aria-controls')).toBe('batch-help-rotate-mode');
});

t('the After patching legend holds its info button beside its words',async()=>{
 const env=await setupRotate({certs:[cert('/s/a_crt','a_crt',5,'inst-a','cache-a')],bootOptions:{noDialogs:true,guidance:true,routes:QUIET}});envs.push(env);
 const {d}=env;
 const legend=d.querySelector('#rotate-mode-converge > legend');
 infoAfter(legend,'rotate-converge');
 expect(legend.querySelector(':scope > .field-guidance-text').textContent).toBe('After patching');
 expect(d.querySelector('#rotate-mode-converge > .field-guidance')).toBe(null);
});

t('the stylesheet puts the Mode help in the strip corner and rows its headings',()=>{
 const css=require('fs').readFileSync(require('path').join(__dirname,'../../ui/view-guidance.css'),'utf8');
 expect(/\.rotate-mode\s*>\s*\.batch-mode-help\s*\{[^}]*position:\s*absolute[^}]*top:[^}]*right:/.test(css)).toBe(true);
 expect(/\.rotate-mode\s*\{[^}]*position:\s*relative/.test(css)).toBe(true);
 expect(/\.field-guidance-row\s*\{[^}]*display:\s*flex[^}]*align-items:\s*center/.test(css)).toBe(true);
});

t('the stylesheet reserves strip room for the corner help and closes the label gap in a flex row',()=>{
 const read=(f)=>require('fs').readFileSync(require('path').join(__dirname,'../../ui/'+f),'utf8');
 expect(/\.rotate-mode\s*>\s*bs-tabs\s*>\s*\[role=tablist\]\s*\{[^}]*padding-right:\s*40px/.test(read('view-guidance.css'))).toBe(true);
 expect(/\.filter-row\s*>\s*label\s*\+\s*\.field-guidance\s*\{[^}]*margin-left:\s*-6px/.test(read('forge-theme.css'))).toBe(true);
});

t('upgrade options and Execution settings each get an info button',async()=>{
 const env=await setupCommand();const {d}=env;
 infoAfter(d.getElementById('batch-additional-title'),'additional-releases');
 infoAfter(d.querySelector('label[for="upgrade-max-batch-jobs"]'),'concurrent-jobs');
 expect(d.querySelectorAll('.batch-execution-settings .field-guidance').length).toBe(1);
});
