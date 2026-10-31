const {test,expect,afterEach}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const {setupRotate,cert}=require('./rotate-flow-support.js');
const t=JSDOM?test:test.skip;

const envs=[];
afterEach(()=>{while(envs.length){const env=envs.pop();expect(env.alerts).toEqual([]);env.w.close();}});
async function setupCommand(probes={}){
 const env=boot({noDialogs:true}),{w}=env;envs.push(env);
 w.__routes.push(['/b/bosh/stemcells',(u,o,j)=>j(200,[])],['/b/bosh/releases',(u,o,j)=>j(200,[])],['/b/bosh/deployment-releases',(u,o,j)=>j(200,{})],['/b/upgrade/settings',(u,o,j)=>j(200,{})],['/b/upgrade/tasks',(u,o,j)=>j(200,[])],
  ['/b/upgrade/commands/preview',(u,o,j)=>{const answer=probes[JSON.parse(o.body).run_as];return answer?j(answer[0],answer[1]):j(400,{success:false,error:'Select at least one instance to preview.',code:'no_selections'});}]);
 await sleep(30);w.serviceInstances={cache:{service_id:'valkey',plan_id:'small'}};
 await w.__T.initUpgradeTab();await sleep(10);
 await w.__T.requestOperation('command');w.__T.setBatchStep(1,false);
 return env;
}
const describedBy=(d,id)=>(d.getElementById(id).getAttribute('aria-describedby')||'').split(/\s+/).map(ref=>d.getElementById(ref)).filter(Boolean).map(el=>el.textContent).join(' ');

t('the timeout and the node limit are described by their hints',async()=>{
 const {d}=await setupCommand();
 expect(describedBy(d,'command-timeout')).toContain('from 10 to 3600');
 expect(describedBy(d,'command-timeout')).toContain('The default is 600');
 expect(describedBy(d,'command-max-in-flight')).toContain('at most 3 with the default of four workers');
 expect(describedBy(d,'command-max-in-flight')).toContain('The review checks it');
 expect(describedBy(d,'command-text')).toContain('call service tools by their full path');
 expect(describedBy(d,'command-parallel')).toContain('a failure stops the run early');
});

t('the root note shows our own sentence when the broker refuses root',async()=>{
 const raw='running commands as root is disabled';
 const {d}=await setupCommand({root:[403,{success:false,error:raw,code:'root_disabled'}]});
 const note=d.getElementById('command-root-note');
 expect(note.textContent).toBe('Running as root is switched off on this broker.');
 expect(note.textContent.includes(raw)).toBe(false);
});

t('the root note explains when to choose root while root is allowed',async()=>{
 const {d}=await setupCommand();
 expect(d.getElementById('command-root-note').textContent).toBe('Root can change anything on the VM, so choose it only when vcap can\'t do the job.');
});

t('the reason hint says where the reason goes for each operation',async()=>{
 const {d}=await setupCommand();
 expect(d.getElementById('rotate-reason-help').textContent).toContain('saved in the audit record');
 const env=await setupRotate({certs:[cert('/s/a_crt','a_crt',5,'inst-a','cache-a')],bootOptions:{noDialogs:true}});envs.push(env);
 const help=env.d.getElementById('rotate-reason-help').textContent;
 expect(help).toContain('saved with the job');
 expect(help).toContain('change ticket');
});

t('operation descriptions say what each operation is for',async()=>{
 const {d}=await setupCommand();
 const text=id=>d.getElementById('rotate-op-'+id).closest('label').querySelector('.batch-operation-description').textContent;
 expect(text('upgrade')).toBe('Move instances to a newer stemcell or service release.');
 expect(text('rotate')).toBe('Replace expiring certificates by redeploy or patch in place.');
 expect(text('converge')).toBe('Finish a patch in place by deploying its certificates through BOSH.');
 expect(d.getElementById('batch-version-floor-help').textContent).toBe('Use a minimum to upgrade only instances already at that version or newer. It narrows the list and never changes the deploy version.');
});

t('Execution settings say what an empty job limit means',async()=>{
 const {d}=await setupCommand();
 const input=d.getElementById('upgrade-max-batch-jobs');
 expect(input.placeholder).toBe('No job limit');
 const help=describedBy(d,'upgrade-max-batch-jobs');
 expect(help).toContain('a paused job still counts against it');
 expect(help).toContain('max_batch_connections');
});

t('the pause on failure setting explains all three kinds of job',()=>{
 const src=require('fs').readFileSync(require('path').join(__dirname,'../../ui/blacksmith.js'),'utf8');
 const line=src.split('\n').find(l=>l.includes("key: 'pause_on_failure'"));
 expect(line.includes('each job can change it on its Options step')).toBe(true);
 expect(line.includes('An upgrade goes on after a failure unless its job turns pausing on')).toBe(true);
 expect(line.includes('Run command always pauses')).toBe(true);
});

t('the command key prompt says why the key is needed',()=>{
 const src=require('fs').readFileSync(require('path').join(__dirname,'../../ui/blacksmith.js'),'utf8');
 expect(src.includes("so that signing in to the broker isn\\'t enough to run code on service VMs")).toBe(true);
});
