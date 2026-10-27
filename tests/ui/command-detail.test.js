const {test,expect,afterEach}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const SECRET='echo SECRET-COMMAND-TEXT-7731';
const KEY='key-SECRET-CREDENTIAL-5519';
const TASK='11111111-1111-4111-8111-111111111111';
const OUT_URL=`/b/upgrade/tasks/${TASK}/output`;
const DETAIL_URL=`/b/upgrade/tasks/${TASK}`;

// Every env a test boots, checked once the test ends so a dialog error that
// the UI code swallows still fails it. Neither secret reaches a console method.
const envs=[];
afterEach(()=>{ while(envs.length){ const env=envs.pop(); expect(env.alerts).toEqual([]);
 expect(env.logs.some(l=>String(l).includes(SECRET)||String(l).includes(KEY))).toBe(false); } });

const hasSecret=(s)=>String(s).includes(SECRET);
const hasKey=(s)=>String(s).includes(KEY);
const noKeyIn=(d)=>!hasKey(d.documentElement.outerHTML)&&[...d.querySelectorAll('input')].every(i=>!hasKey(i.value));
// Looks the input up, proves it exists, then types into it.
const typeKey=(root,sel='input[type="password"]',val=KEY)=>{const i=root.querySelector(sel);expect(i!==null).toBe(true);i.value=val;return i;};
const press=(root,sel)=>{const b=root.querySelector(sel);expect(b!==null).toBe(true);b.click();};
const panel=(d)=>d.querySelector('#upgrade .upgrade-tasks');
const when=(d,s)=>new Date(Date.UTC(2026,9,26,12,0,0)+s*1000).toISOString();

const node=(group,index,over={})=>({reviewed:{group,index,id:'vm-'+index,cid:'c'+index},matched:{group,index,id:'vm-'+index,cid:'c'+index},phase:'settled',status:'succeeded',exit_code:0,started_at:when(0,0),ended_at:when(0,75),stdout_kept:1229,stdout_received:1229,stderr_kept:0,stderr_received:0,output_path:'command-output/x',...over});
const inst=(id,vms,over={})=>({instance_id:id,instance_name:id+'-name',deployment_name:'dep-'+id,status:'success',command:{deployment:'dep-'+id,vms},...over});
const task=(over={})=>({id:TASK,kind:'command',status:'completed',name:'',target:'Run as vcap on 2 nodes',total_count:1,completed_count:1,failed_count:0,created_at:when(0,0),
 command:{command:SECRET,command_digest:'abcdef0123456789abcdef',command_length:SECRET.length,run_as:'vcap',shell:'/bin/bash',timeout_seconds:600,parallel:true,max_in_flight:2,reason:'rolling restart check'},
 instances:[inst('val-a',[node('node',0),node('node',1,{status:'failed',exit_code:3})])],...over});
const output=(over={})=>({stdout:'hello out\n',stderr:'oops err\n',stdout_kept:10,stdout_received:10,stderr_kept:9,stderr_received:9,stdout_truncated:false,stderr_truncated:false,...over});

// routes.detail is the task detail answer, routes.output answers [status, body]
// or a function of (url, options). outputs records each output read.
async function setup(routes={}){
 const env=boot({noDialogs:true}),{w}=env;envs.push(env);
 const outputs=[],resumes=[],details=[];
 const state={detail:routes.detail||task()};
 w.__routes.push(
  ['/b/bosh/stemcells',(u,o,j)=>j(200,[])],['/b/bosh/releases',(u,o,j)=>j(200,[])],['/b/bosh/deployment-releases',(u,o,j)=>j(200,{})],['/b/upgrade/settings',(u,o,j)=>j(200,{})],
  [OUT_URL,(u,o,j)=>{
   outputs.push({url:u,headers:o.headers||{}});
   const spec=routes.output||[200,output()];
   const r=typeof spec==='function'?spec(u,o,outputs.length):spec;
   return j(r[0],r[1]);
  }],
  [`${DETAIL_URL}/resume`,(u,o,j)=>{
   resumes.push({headers:o.headers||{}});
   if(routes.gated&&(o.headers||{})['X-Blacksmith-Command-Key']!==KEY) return j(401,{success:false,error:'a valid command key is required',code:'command_credential_required'});
   return j(200,{success:true});
  }],
  [DETAIL_URL,(u,o,j)=>{
   details.push({headers:o.headers||{}});
   if(routes.gated&&(o.headers||{})['X-Blacksmith-Command-Key']!==KEY) return j(401,{success:false,error:'a valid command key is required',code:'command_credential_required'});
   return j(200,state.detail);
  }],
  ['/b/upgrade/tasks',(u,o,j)=>j(200,[{...state.detail,instances:undefined,command:{command_digest:state.detail.command&&state.detail.command.command_digest,command_length:1,run_as:'vcap',timeout_seconds:600,parallel:false}}])]);
 w.plansData={services:[]};
 await sleep(30);
 await w.__T.initUpgradeTab();await sleep(10);
 return {...env,outputs,resumes,details,state};
}
const open=async(env)=>{await env.w.__T.selectUpgradeTask(TASK);await sleep(10);};
const detailsOf=(d)=>[...d.querySelectorAll('#upgrade .upgrade-tasks details')];
const openDetails=async(env,i=0)=>{const el=detailsOf(env.d)[i];el.open=true;await sleep(30);return el;};
const pageText=(d)=>d.body.textContent;

t('the task list badges a command task with the digest prefix and never the text',async()=>{
 const env=await setup();const {w,d}=env;try{
 const detail=task();
 // A summary that carried the text by mistake must not show it.
 w.__T._setTasks([{...detail,instances:undefined,command:{command:SECRET,command_digest:'abcdef0123456789abcdef',command_length:SECRET.length,run_as:'vcap',timeout_seconds:600,parallel:false}}]);
 w.__T.renderUpgradeTaskList();
 const item=d.querySelector('#upgrade .upgrade-task-item');
 expect(item.textContent.includes('Command')).toBe(true);
 expect(item.textContent.includes('abcdef012345')).toBe(true);
 expect(item.textContent.includes('abcdef0123456')).toBe(false);
 expect(hasSecret(d.body.innerHTML)).toBe(false);
 expect([...d.querySelectorAll('*')].some(e=>[...e.attributes].some(a=>hasSecret(a.value)))).toBe(false);
 }finally{w.close();}
});

t('the detail shows the command in a pre with its settings and a working copy button',async()=>{
 const env=await setup();const {w,d}=env;try{
 const copied=[];
 w.navigator.clipboard={writeText:(x)=>{copied.push(x);return Promise.resolve();}};
 await open(env);
 const host=d.querySelector('#upgrade .upgrade-task-detail');
 const pre=host.querySelector('pre.command-text');
 expect(pre!==null).toBe(true);expect(pre.textContent===SECRET).toBe(true);
 const rest=host.textContent.replace(SECRET,'');
 for(const want of ['Run as','vcap','Shell','/bin/bash','Timeout','600','Parallel','2 at a time','Reason','rolling restart check']) expect(rest.includes(want)).toBe(true);
 // The text is in the pre and nowhere else, not even an attribute.
 const holders=[...d.querySelectorAll('*')].filter(e=>e.children.length===0&&hasSecret(e.textContent));
 expect(holders.every(e=>e.tagName==='PRE')).toBe(true);
 expect([...d.querySelectorAll('*')].some(e=>[...e.attributes].some(a=>hasSecret(a.value)))).toBe(false);
 host.querySelector('button.command-copy').click();await sleep(5);
 expect(copied.length).toBe(1);expect(copied[0]===SECRET).toBe(true);
 }finally{w.close();}
});

t('the copy button does nothing visible without a clipboard',async()=>{
 const env=await setup();const {w,d}=env;try{
 await open(env);
 delete w.navigator.clipboard;
 try{ Object.defineProperty(w.navigator,'clipboard',{value:undefined,configurable:true}); }catch(e){}
 const before=d.body.innerHTML;
 d.querySelector('button.command-copy').click();await sleep(5);
 expect(d.body.innerHTML===before).toBe(true);
 }finally{w.close();}
});

t('the detail renders no command text as markup and escapes record fields',async()=>{
 const evil='<img src=x onerror=alert(1)><script>window.__pwn=1</script>';
 const env=await setup({detail:task({command:{...task().command,command:evil,reason:evil},instances:[inst('val-a',[node('node',0,{reason:evil,error:evil})],{instance_name:evil})]})});const {w,d}=env;try{
 await open(env);
 const host=d.querySelector('#upgrade .upgrade-task-detail');
 expect(host.querySelector('img')===null).toBe(true);
 expect(host.querySelector('script')===null).toBe(true);
 expect(host.querySelector('pre.command-text').textContent===evil).toBe(true);
 expect(w.__pwn===undefined).toBe(true);
 }finally{w.close();}
});

t('each node shows its icon, exit code, duration, and a killed note',async()=>{
 const env=await setup({detail:task({instances:[inst('val-a',[node('node',0),node('node',1,{status:'timed_out',exit_code:137,killed:true,ended_at:when(0,130)})])]})});const {w,d}=env;try{
 await open(env);
 const rows=[...d.querySelectorAll('#upgrade .command-node')];
 expect(rows.length).toBe(2);
 expect(rows[0].textContent.includes('node-0')).toBe(true);
 expect(rows[0].querySelector('.status-icon.success')!==null).toBe(true);
 expect(rows[0].textContent.includes('exit 0')).toBe(true);
 expect(rows[0].textContent.includes('1m 15s')).toBe(true);
 expect(rows[0].textContent.includes('killed')).toBe(false);
 expect(rows[1].textContent.includes('exit 137')).toBe(true);
 expect(rows[1].textContent.includes('2m 10s')).toBe(true);
 expect(rows[1].textContent.includes('killed')).toBe(true);
 }finally{w.close();}
});

t('the output summary reports kept of received, truncation, and empty streams',async()=>{
 const env=await setup({detail:task({instances:[inst('val-a',[
  node('node',0,{stdout_kept:1229,stdout_received:1229,stderr_kept:0,stderr_received:0}),
  node('node',1,{stdout_kept:65536,stdout_received:133120,stdout_truncated:true,stderr_kept:5,stderr_received:5})])]})});const {w,d}=env;try{
 await open(env);
 const s=detailsOf(d).map(x=>x.querySelector('summary').textContent);
 expect(s[0]).toBe('Output (stdout 1.2 KiB, stderr 0 B)');
 expect(s[1].startsWith('Output (stdout 64.0 KiB of 130.0 KiB')).toBe(true);
 expect(s[1].includes('truncated')).toBe(true);
 expect(s[1].includes('stderr 5 B')).toBe(true);
 }finally{w.close();}
});

t('opening a node reads its output with vm and instance, and shows each stream in its own pre',async()=>{
 const env=await setup();const {w,d}=env;try{
 await open(env);
 expect(env.outputs.length).toBe(0);
 const el=await openDetails(env,1);
 expect(env.outputs.length).toBe(1);
 const url=new URL(env.outputs[0].url,'http://localhost');
 expect(url.pathname).toBe(OUT_URL);
 expect(url.searchParams.get('vm')).toBe('node-1');
 expect(url.searchParams.get('instance')).toBe('val-a');
 expect(env.outputs[0].headers['X-Blacksmith-Command-Key']===undefined).toBe(true);
 const pres=[...el.querySelectorAll('pre')];
 expect(pres.length).toBe(2);
 expect(pres[0].textContent).toBe('hello out\n');
 expect(pres[1].textContent).toBe('oops err\n');
 }finally{w.close();}
});

t('output with markup and control bytes reaches the page only as stripped text',async()=>{
 const env=await setup({output:[200,output({stdout:'<script>window.__pwn=1</script>\u0007bell\tkept\nline\u001b[31m\u009bx\u007f',stderr:'<img src=x onerror=alert(1)>'})]});const {w,d}=env;try{
 await open(env);
 const el=await openDetails(env,0);
 expect(el.querySelector('script')===null).toBe(true);
 expect(el.querySelector('img')===null).toBe(true);
 expect(w.__pwn===undefined).toBe(true);
 const pres=[...el.querySelectorAll('pre')];
 expect(pres[0].textContent.includes('<script>')).toBe(true);
 expect(pres[0].textContent.includes('\u0007')).toBe(false);
 expect(pres[0].textContent.includes('\u001b')).toBe(false);
 expect(pres[0].textContent.includes('\u009b')).toBe(false);
 expect(pres[0].textContent.includes('\u007f')).toBe(false);
 expect(pres[0].textContent.includes('bell\tkept\nline')).toBe(true);
 expect(d.documentElement.outerHTML.includes('\u0007')).toBe(false);
 }finally{w.close();}
});

t('a command key prompt opens on 401, and the key is sent, reused, and never leaves the input',async()=>{
 const env=await setup({output:(u,o)=>(o.headers&&o.headers['X-Blacksmith-Command-Key']===KEY)?[200,output()]:[401,{success:false,error:'a command key is required',code:'command_credential_required'}]});const {w,d}=env;try{
 await open(env);
 const el=await openDetails(env,0);
 const input=el.querySelector('input[type="password"]');
 expect(input!==null).toBe(true);
 expect(el.textContent.includes('asks for a command key')).toBe(true);
 const button=[...el.querySelectorAll('button')].find(b=>b.textContent==='Show output');
 expect(button!==undefined).toBe(true);
 input.value=KEY;expect(button!==undefined).toBe(true);button.click();await sleep(30);
 expect(env.outputs.length).toBe(2);
 expect(env.outputs[1].headers['X-Blacksmith-Command-Key']===KEY).toBe(true);
 expect(el.querySelectorAll('pre').length).toBe(2);
 // The next node reuses the key without a prompt.
 const second=await openDetails(env,1);
 expect(env.outputs.length).toBe(3);
 expect(env.outputs[2].headers['X-Blacksmith-Command-Key']===KEY).toBe(true);
 expect(second.querySelector('input[type="password"]')===null).toBe(true);
 // Only the input ever held it, and not as an attribute or in a form field after use.
 expect(hasKey(d.documentElement.outerHTML)).toBe(false);
 expect([...d.querySelectorAll('input')].every(i=>!hasKey(i.value))).toBe(true);
 const keep=(s)=>{try{return s.getItem?[...Array(s.length)].map((_,i)=>s.getItem(s.key(i))).join('|'):'';}catch(e){return '';}};
 expect(hasKey(keep(w.localStorage))||hasKey(keep(w.sessionStorage))).toBe(false);
 expect(hasKey(w.location.href)).toBe(false);
 expect(hasKey(JSON.stringify(w.__T.batchFlow.drafts))).toBe(false);
 expect(env.outputs.every(r=>!hasKey(r.url))).toBe(true);
 }finally{w.close();}
});

t('a rejected key blanks every key input, drops the output, and asks for the key again',async()=>{
 const env=await setup({output:[401,{success:false,error:'a command key is required',code:'command_credential_required'}]});const {w,d}=env;try{
 await open(env);
 const first=await openDetails(env,0);
 const second=await openDetails(env,1);
 const other=second.querySelector('input[type="password"]');
 expect(other!==null).toBe(true);other.value=KEY+'-typed';
 typeKey(first);
 const show=[...first.querySelectorAll('button')].find(b=>b.textContent==='Show output');expect(show!==undefined).toBe(true);show.click();await sleep(30);
 expect(env.outputs[2].headers['X-Blacksmith-Command-Key']===KEY).toBe(true);
 expect(other.value==='').toBe(true);
 expect(panel(d).textContent.includes('did not accept')).toBe(true);
 expect(panel(d).querySelector('pre')===null).toBe(true);
 expect(hasSecret(d.body.textContent)).toBe(false);
 expect(noKeyIn(d)).toBe(true);
 // The key is gone, so the next read of the detail goes out without it.
 await w.__T.selectUpgradeTask(TASK);await sleep(10);
 expect(env.details[env.details.length-1].headers['X-Blacksmith-Command-Key']===undefined).toBe(true);
 }finally{w.close();}
});

t('switching to another top-level tab drops the key and leaves no command text or output on the page',async()=>{
 const env=await setup({gated:true,output:(u,o)=>(o.headers&&o.headers['X-Blacksmith-Command-Key']===KEY)?[200,output()]:[401,{success:false,error:'k',code:'command_credential_required'}]});const {w,d}=env;try{
 await w.__T.selectUpgradeTask(TASK);await sleep(10);
 typeKey(panel(d));press(panel(d),'button.command-output-show');await sleep(40);
 const first=await openDetails(env,0);
 expect(first.querySelectorAll('pre').length).toBe(2);
 const stray=first.ownerDocument.createElement('input');stray.type='password';stray.className='command-output-key';stray.value=KEY;first.appendChild(stray);expect(first.contains(stray)).toBe(true);
 d.querySelector('.tab-button[data-tab="services"]').click();await sleep(30);
 expect(stray.value==='').toBe(true);
 expect(noKeyIn(d)).toBe(true);
 expect(hasSecret(d.body.textContent)).toBe(false);
 expect(d.body.textContent.includes('hello out')).toBe(false);
 expect(d.querySelector('pre.command-text')===null).toBe(true);
 // Coming back asks for the key before it shows anything.
 const reads=env.details.length;
 d.querySelector('.tab-button[data-tab="upgrade"]').click();await sleep(60);
 expect(d.body.textContent.includes('hello out')).toBe(false);
 expect(panel(d).querySelector('input[type="password"]')!==null).toBe(true);
 expect(env.details.slice(reads).every(r=>r.headers['X-Blacksmith-Command-Key']===undefined)).toBe(true);
 // With the key entered again the output is read again, not shown from memory.
 const before=env.outputs.length;
 typeKey(panel(d));press(panel(d),'button.command-output-show');await sleep(40);
 await openDetails(env,0);
 expect(env.outputs.length).toBe(before+1);
 }finally{w.close();}
});

t('a gated detail asks for the key inline, sends it, and keeps the key off the page',async()=>{
 const env=await setup({gated:true});const {w,d}=env;try{
 const ok=await w.__T.selectUpgradeTask(TASK);await sleep(10);
 expect(ok).toBe(false);
 expect(panel(d).textContent.includes('asks for a command key')).toBe(true);
 expect(panel(d).textContent.includes('Could not load')).toBe(false);
 expect(env.details.length).toBe(1);
 // The poll does not repeat the refused read while it waits for a key.
 await w.__T.fetchUpgradeTasks();await w.__T.fetchUpgradeTasks();await sleep(20);
 expect(env.details.length).toBe(1);
 typeKey(panel(d));press(panel(d),'button.command-output-show');await sleep(40);
 expect(env.details.length).toBe(2);
 expect(env.details[1].headers['X-Blacksmith-Command-Key']===KEY).toBe(true);
 expect(panel(d).querySelector('pre.command-text')!==null).toBe(true);
 // The poll's detail read carries the key from now on.
 env.state.detail=task({status:'running'});
 await w.__T.fetchUpgradeTasks();await sleep(20);
 expect(env.details[env.details.length-1].headers['X-Blacksmith-Command-Key']===KEY).toBe(true);
 expect(noKeyIn(d)).toBe(true);
 }finally{w.close();}
});

t('a detail the broker refuses a held key for says so, drops the key, and stops',async()=>{
 const env=await setup({gated:true});const {w,d}=env;try{
 await w.__T.selectUpgradeTask(TASK);await sleep(10);
 typeKey(panel(d),'input[type="password"]','wrong-'+KEY);press(panel(d),'button.command-output-show');await sleep(40);
 expect(panel(d).textContent.includes('did not accept')).toBe(true);
 const n=env.details.length;
 await w.__T.fetchUpgradeTasks();await sleep(20);
 expect(env.details.length).toBe(n);
 expect(d.documentElement.outerHTML.includes('wrong-'+KEY)).toBe(false);
 }finally{w.close();}
});

t('a poll that meets the gate shows the prompt and stops reading',async()=>{
 const env=await setup({gated:true});const {w,d}=env;try{
 // The detail is on screen because the first read was allowed.
 w.__routes.unshift([new RegExp(DETAIL_URL+'$'),(u,o,j)=>{ env.details.push({headers:o.headers||{}}); return env.details.length===1?j(200,task({status:'running'})):j(401,{success:false,error:'k',code:'command_credential_required'}); }]);
 await w.__T.selectUpgradeTask(TASK);await sleep(10);
 expect(panel(d).querySelector('pre.command-text')!==null).toBe(true);
 await w.__T.fetchUpgradeTasks();await sleep(20);
 expect(panel(d).querySelector('input[type="password"]')!==null).toBe(true);
 const n=env.details.length;
 await w.__T.fetchUpgradeTasks();await sleep(20);
 expect(env.details.length).toBe(n);
 }finally{w.close();}
});

t('resume of a command task sends the held key, and without it asks for one and waits for Continue',async()=>{
 const paused=task({status:'paused',failed_count:1,instances:[inst('val-a',[node('node',0,{status:'failed',exit_code:3})],{status:'failed'}),inst('val-b',[node('node',0,{phase:'pending',status:''})],{status:'pending'})]});
 const env=await setup({detail:paused,gated:true});const {w,d}=env;try{
 w.__routes.unshift([new RegExp(DETAIL_URL+'$'),(u,o,j)=>{ env.details.push({headers:o.headers||{}}); return j(200,paused); }]);
 await w.__T.selectUpgradeTask(TASK);await sleep(10);
 press(d,'.resume-btn');await sleep(30);
 expect(env.resumes.length).toBe(1);
 expect(env.resumes[0].headers['X-Blacksmith-Command-Key']===undefined).toBe(true);
 const prompt=panel(d).querySelector('.command-key-prompt');
 expect(prompt!==null).toBe(true);
 expect(prompt.textContent.includes('asks for a command key')).toBe(true);
 typeKey(prompt,'input');
 press(prompt,'button');await sleep(40);
 // The key only reopens the task. No resume went out with it.
 expect(env.resumes.length).toBe(1);
 expect(panel(d).querySelector('.command-key-prompt')===null).toBe(true);
 press(d,'.resume-btn');await sleep(30);
 expect(env.resumes.length).toBe(2);
 expect(env.resumes[1].headers['X-Blacksmith-Command-Key']===KEY).toBe(true);
 press(d,'.resume-btn');await sleep(30);
 expect(env.resumes.length).toBe(3);
 expect(env.resumes[2].headers['X-Blacksmith-Command-Key']===KEY).toBe(true);
 expect(noKeyIn(d)).toBe(true);
 }finally{w.close();}
});

t('a resume the broker rejects the key for drops the key, and entering it again re-reads without resuming',async()=>{
 const paused=task({status:'paused',failed_count:1,instances:[inst('val-a',[node('node',0,{status:'failed',exit_code:3})],{status:'failed'})]});
 const env=await setup({detail:paused});const {w,d}=env;try{
 await w.__T.selectUpgradeTask(TASK);await sleep(10);
 env.w.__routes.unshift([`${DETAIL_URL}/resume`,(u,o,j)=>{env.resumes.push({headers:o.headers||{}});return (o.headers||{})['X-Blacksmith-Command-Key']===KEY&&env.resumes.length>2?j(200,{success:true}):j(401,{success:false,error:'k',code:'command_credential_required'});}]);
 press(d,'.resume-btn');await sleep(30);
 const prompt=panel(d).querySelector('.command-key-prompt');
 expect(prompt!==null).toBe(true);
 typeKey(prompt,'input');press(prompt,'button');await sleep(40);
 expect(env.resumes.length).toBe(1);
 // Pressing Continue sends the key, the broker refuses it, and the key goes.
 press(d,'.resume-btn');await sleep(30);
 expect(env.resumes.length).toBe(2);
 expect(panel(d).textContent.includes('did not accept')).toBe(true);
 expect(panel(d).querySelector('pre.command-text')===null).toBe(true);
 expect(noKeyIn(d)).toBe(true);
 // Entering the key again re-reads the task and sends no resume.
 const reads=env.details.length;
 typeKey(panel(d),'input[type="password"]');press(panel(d),'button.command-output-show');await sleep(40);
 expect(env.resumes.length).toBe(2);
 expect(env.details.length>reads).toBe(true);
 expect(panel(d).querySelector('pre.command-text')!==null).toBe(true);
 // Continue then sends one resume with the key.
 press(d,'.resume-btn');await sleep(30);
 expect(env.resumes.length).toBe(3);
 expect(env.resumes[2].headers['X-Blacksmith-Command-Key']===KEY).toBe(true);
 }finally{w.close();}
});

t('cancelling or leaving the tab while a resume prompt is up never leads to a resume',async()=>{
 const paused=task({status:'paused',failed_count:1,instances:[inst('val-a',[node('node',0,{status:'failed',exit_code:3})],{status:'failed'})]});
 const env=await setup({detail:paused,gated:true});const {w,d}=env;try{
 w.__routes.unshift([new RegExp(DETAIL_URL+'$'),(u,o,j)=>{ env.details.push({headers:o.headers||{}}); return j(200,paused); }]);
 await w.__T.selectUpgradeTask(TASK);await sleep(10);
 press(d,'.resume-btn');await sleep(30);
 expect(env.resumes.length).toBe(1);
 expect(panel(d).querySelector('.command-key-prompt')!==null).toBe(true);
 d.querySelector('.tab-button[data-tab="services"]').click();await sleep(30);
 d.querySelector('.tab-button[data-tab="upgrade"]').click();await sleep(60);
 expect(env.resumes.length).toBe(1);
 expect(noKeyIn(d)).toBe(true);
 }finally{w.close();}
});

t('the prompt and Continue buttons wait for their request, so a second press sends nothing',async()=>{
 const paused=task({status:'paused',failed_count:1,instances:[inst('val-a',[node('node',0,{status:'failed',exit_code:3})],{status:'failed'})]});
 const env=await setup({detail:paused});const {w,d}=env;try{
 await w.__T.selectUpgradeTask(TASK);await sleep(10);
 let release;const gate=new Promise((r)=>{release=r;});
 w.__routes.unshift([`${DETAIL_URL}/resume`,async(u,o,j)=>{env.resumes.push({headers:o.headers||{}});await gate;return j(200,{success:true});}]);
 const btn=d.querySelector('.resume-btn');expect(btn!==null).toBe(true);
 btn.click();await sleep(5);btn.click();await sleep(5);btn.click();await sleep(20);
 expect(btn.disabled).toBe(true);
 expect(env.resumes.length).toBe(1);
 release();await sleep(30);
 }finally{w.close();}
});

t('hidden control and bidi characters in the command and reason show as escapes, in the detail and the review',async()=>{
 const text='echo a\u0007b\u001Bc‮d\ne\tf';
 const env=await setup({detail:task({command:{...task().command,command:text,reason:'why‮here'}})});const {w,d}=env;try{
 const copied=[];w.navigator.clipboard={writeText:(x)=>{copied.push(x);return Promise.resolve();}};
 await open(env);
 const pre=d.querySelector('#upgrade pre.command-text');
 expect(pre.textContent==='echo a\\u0007b\\u001Bc\\u202Ed\ne\tf').toBe(true);
 expect(d.querySelector('#upgrade .command-hidden-note')!==null).toBe(true);
 const reason=[...d.querySelectorAll('#upgrade .summary-row')].find(r=>r.textContent.startsWith('Reason'));
 expect(reason.textContent.includes('‮')).toBe(false);expect(reason.textContent.includes('\\u202E')).toBe(true);
 expect(/[\u0007\u001b‮]/.test(d.querySelector('#upgrade .upgrade-task-detail').textContent)).toBe(false);
 // Copy still sends the real text.
 d.querySelector('button.command-copy').click();await sleep(5);
 expect(copied[0]===text).toBe(true);
 // The review shows the same, and what is sent stays unchanged.
 const review=w.__T.buildCommandReviewBody({instances:[],warnings:[],run_as:'vcap',shell:'/bin/bash',timeout_seconds:600},{command:text,run_as:'vcap',timeout_seconds:600,parallel:false});
 expect(review.querySelector('pre').textContent==='echo a\\u0007b\\u001Bc\\u202Ed\ne\tf').toBe(true);
 expect(review.querySelector('.command-hidden-note')!==null).toBe(true);
 expect(/[\u0007\u001b\u202E]/.test(review.textContent)).toBe(false);
 }finally{w.close();}
});

t('stdout and stderr lose every C0 and C1 control character except tab and newline',async()=>{
 let all='';for(let c=0;c<0xA0;c++) all+=String.fromCharCode(c);
 const env=await setup({output:[200,output({stdout:'<'+all+'>',stderr:'['+all+']'})]});const {w,d}=env;try{
 await open(env);
 const el=await openDetails(env,0);
 const pres=[...el.querySelectorAll('pre')];
 expect(pres.length).toBe(2);
 let keep='';for(let c=0x20;c<0x7F;c++) keep+=String.fromCharCode(c);
 const expectOut=(open,close)=>open+'\t\n'+keep+close;
 expect(pres[0].textContent===expectOut('<','>')).toBe(true);
 expect(pres[1].textContent===expectOut('[',']')).toBe(true);
 }finally{w.close();}
});

t('cancel of a command task names what it does',async()=>{
 const running=task({status:'running'});
 const env=await setup({detail:running});const {w,d}=env;try{
 await open(env);
 d.querySelector('.cancel-btn').click();await sleep(20);
 const overlay=d.getElementById('dialog-modal-overlay');
 expect(overlay.querySelector('.dialog-modal-title').textContent).toBe('Cancel command task');
 expect(overlay.querySelector('.dialog-modal-message').textContent.includes('stay reserved')||overlay.querySelector('.dialog-modal-message').textContent.includes('stays reserved')).toBe(true);
 expect(overlay.querySelector('.dialog-modal-message').textContent.includes('BOSH')).toBe(false);
 expect(overlay.querySelector('[data-dialog-role="confirm"]').textContent).toBe('Stop the command');
 overlay.querySelector('[data-dialog-role="cancel"]').click();await sleep(10);
 }finally{w.close();}
});

t('a pause of a command task is announced',async()=>{
 const env=await setup({detail:task({status:'running'})});const {w,d}=env;try{
 w.__routes.unshift([`${DETAIL_URL}/pause`,(u,o,j)=>j(200,{success:true})]);
 await open(env);
 d.querySelector('.pause-btn').click();await sleep(60);
 expect(d.getElementById('blacksmith-live-region').textContent.includes('Pause requested')).toBe(true);
 }finally{w.close();}
});

t('a 403 shows the refusal inside the details',async()=>{
 const env=await setup({output:[403,{success:false,error:'commands are switched off',code:'commands_disabled'}]});const {w,d}=env;try{
 await open(env);
 const el=await openDetails(env,0);
 expect(el.textContent.includes('commands are switched off')).toBe(true);
 expect(hasSecret(el.textContent)).toBe(false);
 }finally{w.close();}
});

t('a 404, other errors, and a rejected fetch show a short error with Retry that reads again',async()=>{
 for(const answer of [[404,{success:false,error:'no output',code:'not_found'}],[500,{success:false,error:'broker trouble',code:'internal_error'}],'reject']){
  const env=await setup({output:answer==='reject'?()=>{throw new Error('boom');}:answer});const {w,d}=env;try{
  if(answer==='reject') w.__routes.unshift([OUT_URL,()=>Promise.reject(new Error('network down'))]);
  await open(env);
  const el=await openDetails(env,0);
  const retry=[...el.querySelectorAll('button')].find(b=>b.textContent==='Retry');
  expect(retry!==undefined).toBe(true);
  expect(el.querySelectorAll('pre').length).toBe(0);
  expect(hasSecret(el.textContent)).toBe(false);
  if(answer!=='reject'){ const before=env.outputs.length;retry.click();await sleep(30);expect(env.outputs.length).toBe(before+1); }
  }finally{w.close();}
 }
});

t('a node that has not settled says its output appears later and sends no request',async()=>{
 const env=await setup({detail:task({status:'running',instances:[inst('val-a',[node('node',0,{phase:'sent',status:'',exit_code:undefined,ended_at:undefined}),node('node',1,{phase:'pending',status:''})],{status:'running'})]})});const {w,d}=env;try{
 await open(env);
 const el=await openDetails(env,0);
 expect(el.textContent.includes('appears when the node finishes')).toBe(true);
 expect(el.querySelector('summary').textContent).toBe('Output');
 await openDetails(env,1);
 expect(env.outputs.length).toBe(0);
 }finally{w.close();}
});

t('a re-render keeps fetched output, keeps the details open, and sends no second read',async()=>{
 const env=await setup();const {w,d}=env;try{
 await open(env);
 const el=await openDetails(env,0);
 expect(env.outputs.length).toBe(1);
 // The task changes, so the poll redraws the detail.
 env.state.detail=task({status:'running',completed_count:0});
 await w.__T.fetchUpgradeTasks();await sleep(40);
 const again=detailsOf(d)[0];
 expect(again!==el).toBe(true);
 expect(again.open).toBe(true);
 expect(again.querySelectorAll('pre').length).toBe(2);
 expect(again.querySelectorAll('pre')[0].textContent).toBe('hello out\n');
 expect(env.outputs.length).toBe(1);
 }finally{w.close();}
});

t('a paused command task with a failed instance offers Continue remaining instances and Cancel',async()=>{
 const env=await setup({detail:task({status:'paused',failed_count:1,instances:[inst('val-a',[node('node',0,{status:'failed',exit_code:3})],{status:'failed'}),inst('val-b',[node('node',0,{phase:'pending',status:''})],{status:'pending'})]})});const {w,d}=env;try{
 await open(env);
 const text=[...d.querySelectorAll('#upgrade .job-control-btn')].map(b=>b.textContent);
 expect(text.includes('Continue remaining instances')).toBe(true);
 expect(text.some(x=>x.includes('Cancel'))).toBe(true);
 expect(text.includes('Resume')).toBe(false);
 }finally{w.close();}
});

t('a paused command task with a running node reads Pause requested',async()=>{
 const env=await setup({detail:task({status:'paused',failed_count:0,instances:[inst('val-a',[node('node',0,{phase:'sent',status:''})],{status:'running'})]})});const {w,d}=env;try{
 await open(env);
 expect(d.querySelector('#upgrade .task-detail-summary').textContent.includes('Pause requested')).toBe(true);
 }finally{w.close();}
});

t('a paused command task with a claimed pending node reads Pause requested',async()=>{
 const env=await setup({detail:task({status:'paused',failed_count:0,instances:[inst('val-a',[node('node',0,{status:'succeeded'}),node('node',1,{phase:'pending',status:'',in_flight:true})],{status:'running'})]})});const {w,d}=env;try{
 await open(env);
 const summary=d.querySelector('#upgrade .task-detail-summary').textContent;
 expect(summary.includes('Pause requested')).toBe(true);
 }finally{w.close();}
});

t('a paused command task with no node in flight reads Paused',async()=>{
 const env=await setup({detail:task({status:'paused',failed_count:0,instances:[inst('val-a',[node('node',0,{status:'succeeded'}),node('node',1,{phase:'pending',status:''})],{status:'running'})]})});const {w,d}=env;try{
 await open(env);
 const summary=d.querySelector('#upgrade .task-detail-summary').textContent;
 expect(summary.includes('Pause requested')).toBe(false);
 expect(summary.includes('Paused')).toBe(true);
 }finally{w.close();}
});

t('a status change of an unnamed command task is announced as a command task',async()=>{
 const env=await setup({detail:task({status:'running',target:''})});const {w,d}=env;try{
 await w.__T.fetchUpgradeTasks();await sleep(20);
 env.state.detail=task({status:'paused',target:''});
 await w.__T.fetchUpgradeTasks();await sleep(120);
 const said=d.getElementById('blacksmith-live-region').textContent;
 expect(said.includes('Command task is now paused')).toBe(true);
 expect(said.includes('Upgrade task')).toBe(false);
 }finally{w.close();}
});

for(const state of ['disabled','loading','error']){
 t(`a command task detail and its output read work with the certificate inventory ${state}`,async()=>{
  const env=await setup();const {w,d}=env;try{
  w.__T.rotateState.loadState=state;
  await open(env);
  const summary=d.querySelector('#upgrade .task-detail-summary');
  expect(summary!==null).toBe(true);
  expect(summary.textContent.includes('Pause requested')).toBe(false);
  const el=await openDetails(env,0);
  expect(env.outputs.length).toBe(1);
  const pres=[...el.querySelectorAll('pre')];
  expect(pres.length).toBe(2);
  expect(pres[0].textContent).toBe('hello out\n');
  }finally{w.close();}
 });
}

t('the page never holds the command text outside the detail pre after the whole flow',async()=>{
 const env=await setup();const {w,d}=env;try{
 await open(env);await openDetails(env,0);
 const holders=[...d.querySelectorAll('*')].filter(e=>e.children.length===0&&hasSecret(e.textContent));
 expect(holders.every(e=>e.tagName==='PRE'&&e.classList.contains('command-text'))).toBe(true);
 expect(env.outputs.every(r=>!hasSecret(r.url)&&!hasSecret(JSON.stringify(r.headers)))).toBe(true);
 expect(env.logs.some(hasSecret)).toBe(false);
 expect(env.calls.some(hasSecret)).toBe(false);
 }finally{w.close();}
});

// Enters the key on the inline prompt and waits for the task to load.
const unlock=async(d)=>{typeKey(panel(d));press(panel(d),'button.command-output-show');await sleep(40);};

t('every hidden character shows as an escape in the command, the reason, and the review',async()=>{
 const codes=[];
 for(let c=0;c<=0x08;c++) codes.push(c);
 for(let c=0x0B;c<=0x1F;c++) codes.push(c);
 for(let c=0x7F;c<=0x9F;c++) codes.push(c);
 codes.push(0x200E,0x200F);
 for(let c=0x202A;c<=0x202E;c++) codes.push(c);
 for(let c=0x2066;c<=0x2069;c++) codes.push(c);
 const raw=codes.map(c=>String.fromCharCode(c)).join('');
 const esc=(c)=>'\\u'+c.toString(16).toUpperCase().padStart(4,'0');
 const want=codes.map(esc).join('');
 const env=await setup({detail:task({command:{...task().command,command:'ok',reason:raw}})});const {w,d}=env;try{
 await open(env);
 const host=d.querySelector('#upgrade .upgrade-task-detail');
 // A clean command still gets a note about the reason.
 const reasonRow=[...host.querySelectorAll('.summary-row')].find(r=>r.textContent.startsWith('Reason'));
 expect(reasonRow!==undefined).toBe(true);
 expect(reasonRow.textContent.includes(want)).toBe(true);
 expect(reasonRow.querySelector('.command-hidden-note')!==null).toBe(true);
 expect(codes.some(c=>host.textContent.includes(String.fromCharCode(c)))).toBe(false);
 expect(host.querySelectorAll('.command-hidden-note').length).toBe(1);
 env.state.detail=task({command:{...task().command,command:raw,reason:'clean'}});
 await w.__T.selectUpgradeTask(TASK);await sleep(10);
 const pre=d.querySelector('#upgrade pre.command-text');
 expect(pre.textContent===want).toBe(true);
 expect(codes.some(c=>d.querySelector('#upgrade .upgrade-task-detail').textContent.includes(String.fromCharCode(c)))).toBe(false);
 const review=w.__T.buildCommandReviewBody({instances:[],warnings:[],run_as:'vcap',shell:'/bin/bash',timeout_seconds:600},{command:raw,run_as:'vcap',timeout_seconds:600,parallel:false});
 expect(review.querySelector('pre').textContent===want).toBe(true);
 expect(codes.some(c=>review.textContent.includes(String.fromCharCode(c)))).toBe(false);
 }finally{w.close();}
});

t('a tab switch clears the cached detail and a read held in flight across it leaves nothing behind',async()=>{
 const env=await setup({gated:true});const {w,d}=env;try{
 await w.__T.selectUpgradeTask(TASK);await sleep(10);
 await unlock(d);
 expect(w.__T.taskDetailCache.get(TASK)!==undefined).toBe(true);
 let release;const gate=new Promise((r)=>{release=r;});
 w.__routes.unshift([new RegExp(DETAIL_URL+'$'),async(u,o,j)=>{env.details.push({headers:o.headers||{}});await gate;return j(200,task());}]);
 const pending=w.__T.selectUpgradeTask(TASK);
 await sleep(5);
 d.querySelector('.tab-button[data-tab="services"]').click();await sleep(20);
 expect(w.__T.taskDetailCache.get(TASK)===undefined).toBe(true);
 release();await pending;await sleep(30);
 expect(hasSecret(d.body.textContent)).toBe(false);
 expect(hasSecret(JSON.stringify([...w.__T.taskDetailCache]))).toBe(false);
 expect(w.__T.taskDetailCache.get(TASK)===undefined).toBe(true);
 expect(noKeyIn(d)).toBe(true);
 }finally{w.close();}
});

t('an output read held in flight across a rejected key paints nothing and is read again after the key returns',async()=>{
 const env=await setup({gated:true,output:(u,o)=>(o.headers&&o.headers['X-Blacksmith-Command-Key']===KEY)?[200,output()]:[401,{success:false,error:'k',code:'command_credential_required'}]});const {w,d}=env;try{
 await w.__T.selectUpgradeTask(TASK);await sleep(10);
 await unlock(d);
 let release;const gate=new Promise((r)=>{release=r;});
 w.__routes.unshift([OUT_URL,async(u,o,j)=>{env.outputs.push({url:u,headers:o.headers||{}});await gate;return j(200,output());}]);
 const el=await openDetails(env,0);
 expect(env.outputs.length).toBe(1);
 // The poll meets a refusal of the held key while the output read waits.
 let refuse=true;
 w.__routes.unshift([new RegExp(DETAIL_URL+'$'),(u,o,j)=>{env.details.push({headers:o.headers||{}});return refuse?j(401,{success:false,error:'k',code:'command_credential_required'}):j(200,task());}]);
 env.state.detail=task({status:'running'});
 await w.__T.fetchUpgradeTasks();await sleep(30);
 expect(panel(d).textContent.includes('did not accept')).toBe(true);
 release();await sleep(30);
 expect(d.body.textContent.includes('hello out')).toBe(false);
 expect(hasSecret(d.body.textContent)).toBe(false);
 // With the key entered again the output is read again, not taken from memory.
 refuse=false;
 w.__routes.shift();
 w.__routes.unshift([OUT_URL,(u,o,j)=>{env.outputs.push({url:u,headers:o.headers||{}});return j(200,output());}]);
 await unlock(d);
 await openDetails(env,0);
 expect(env.outputs.length).toBe(2);
 expect(d.body.textContent.includes('hello out')).toBe(true);
 }finally{w.close();}
});

t('a rejected key blanks stray key inputs, purges the caches, and the next read carries no key',async()=>{
 const env=await setup({gated:true});const {w,d}=env;try{
 await w.__T.selectUpgradeTask(TASK);await sleep(10);
 await unlock(d);
 const stray=d.createElement('input');stray.type='password';stray.className='command-output-key';stray.value=KEY;d.querySelector('#upgrade').appendChild(stray);
 expect(d.querySelector('#upgrade').contains(stray)).toBe(true);
 w.__routes.unshift([new RegExp(DETAIL_URL+'$'),(u,o,j)=>{env.details.push({headers:o.headers||{}});return j(401,{success:false,error:'k',code:'command_credential_required'});}]);
 env.state.detail=task({status:'running'});
 await w.__T.fetchUpgradeTasks();await sleep(30);
 expect(stray.value==='').toBe(true);
 expect(w.__T.taskDetailCache.get(TASK)===undefined).toBe(true);
 expect(hasSecret(d.body.textContent)).toBe(false);
 expect(noKeyIn(d)).toBe(true);
 const before=env.details.length;
 await w.__T.selectUpgradeTask(TASK);await sleep(10);
 expect(env.details.length>before).toBe(true);
 expect(env.details[env.details.length-1].headers['X-Blacksmith-Command-Key']===undefined).toBe(true);
 }finally{w.close();}
});

t('only command tasks are sent the key, and the holder cache never keeps the command text',async()=>{
 const CERT='22222222-2222-4222-8222-222222222222';
 const env=await setup({gated:true});const {w,d}=env;try{
 press(d,'.tab-button[data-tab="upgrade"]');await sleep(30);
 await w.__T.selectUpgradeTask(TASK);await sleep(10);
 await unlock(d);
 const certReads=[];
 w.__routes.unshift([new RegExp(CERT+'$'),(u,o,j)=>{certReads.push({headers:o.headers||{}});return j(200,{id:CERT,kind:'certificates',status:'completed',name:'',target:'x',total_count:0,completed_count:0,failed_count:0,created_at:when(0,0),instances:[]});}]);
 await w.__T.selectUpgradeTask(CERT);await sleep(20);
 expect(certReads.length).toBe(1);
 expect(certReads[0].headers['X-Blacksmith-Command-Key']===undefined).toBe(true);
 // A poll with an active command task reads its holder detail with the key and stores no text.
 env.state.detail=task({status:'running'});
 await w.__T.fetchUpgradeTasks();await sleep(40);
 const keyed=env.details.filter(r=>r.headers['X-Blacksmith-Command-Key']===KEY);
 expect(keyed.length>0).toBe(true);
 expect(w.__T.holderDetails.size>0).toBe(true);
 expect(hasSecret(JSON.stringify([...w.__T.holderDetails]))).toBe(false);
 expect(hasKey(JSON.stringify([...w.__T.holderDetails]))).toBe(false);
 }finally{w.close();}
});

t('pause and cancel requests carry no key even while one is held',async()=>{
 const env=await setup({gated:true,detail:task({status:'running'})});const {w,d}=env;try{
 const seen=[];
 w.__routes.unshift([`${DETAIL_URL}/pause`,(u,o,j)=>{seen.push({verb:'pause',headers:o.headers||{}});return j(200,{success:true});}]);
 w.__routes.unshift([new RegExp('/'+TASK+'/cancel$'),(u,o,j)=>{seen.push({verb:'cancel',headers:o.headers||{}});return j(200,{success:true});}]);
 await w.__T.selectUpgradeTask(TASK);await sleep(10);
 await unlock(d);
 press(d,'.pause-btn');await sleep(40);
 press(d,'.cancel-btn');await sleep(20);
 const confirm=d.getElementById('dialog-modal-overlay').querySelector('[data-dialog-role="confirm"]');
 expect(confirm!==null).toBe(true);confirm.click();await sleep(40);
 expect(seen.map(s=>s.verb).sort()).toEqual(['cancel','pause']);
 expect(seen.every(s=>s.headers['X-Blacksmith-Command-Key']===undefined)).toBe(true);
 }finally{w.close();}
});

const OTHER='33333333-3333-4333-8333-333333333333';

t('a detail read that answers after the operator left the task paints nothing and draws no prompt',async()=>{
 for(const gated of [false,true]){
  const env=await setup({gated});const {w,d}=env;try{
  let release;const gate=new Promise((r)=>{release=r;});
  w.__routes.unshift([new RegExp(DETAIL_URL+'$'),async(u,o,j)=>{env.details.push({headers:o.headers||{}});await gate;return gated?j(401,{success:false,error:'k',code:'command_credential_required'}):j(200,task());}]);
  const pending=w.__T.selectUpgradeTask(TASK);await sleep(5);
  w.__T._setSelectedTask(OTHER);
  release();await pending;await sleep(30);
  expect(panel(d).querySelector('pre.command-text')===null).toBe(true);
  expect(panel(d).querySelector('.command-key-prompt')===null).toBe(true);
  expect(hasSecret(d.body.textContent)).toBe(false);
  }finally{w.close();}
 }
});

t('a holder read held in flight across a dropped key stores nothing',async()=>{
 const env=await setup({gated:true});const {w,d}=env;try{
 press(d,'.tab-button[data-tab="upgrade"]');await sleep(30);
 await w.__T.selectUpgradeTask(TASK);await sleep(10);
 await unlock(d);
 w.__T._setSelectedTask(null);
 let release;const gate=new Promise((r)=>{release=r;});
 w.__routes.unshift([new RegExp(DETAIL_URL+'$'),async(u,o,j)=>{env.details.push({headers:o.headers||{}});await gate;return j(200,task({status:'running'}));}]);
 env.state.detail=task({status:'running'});
 await w.__T.fetchUpgradeTasks();await sleep(20);
 const held=env.details[env.details.length-1];
 expect(held.headers['X-Blacksmith-Command-Key']===KEY).toBe(true);
 press(d,'.tab-button[data-tab="services"]');await sleep(20);
 release();await sleep(30);
 expect(w.__T.holderDetails.has(TASK)).toBe(false);
 }finally{w.close();}
});

t('a key prompt button waits for its request, so a second press sends nothing',async()=>{
 const env=await setup({gated:true});const {w,d}=env;try{
 await w.__T.selectUpgradeTask(TASK);await sleep(10);
 let release;const gate=new Promise((r)=>{release=r;});
 w.__routes.unshift([new RegExp(DETAIL_URL+'$'),async(u,o,j)=>{env.details.push({headers:o.headers||{}});await gate;return j(200,task());}]);
 const before=env.details.length;
 const input=typeKey(panel(d));
 const button=panel(d).querySelector('button.command-output-show');expect(button!==null).toBe(true);
 button.click();await sleep(10);
 expect(button.disabled&&input.disabled).toBe(true);
 button.click();button.click();await sleep(10);
 expect(env.details.length).toBe(before+1);
 release();await sleep(40);
 }finally{w.close();}
});

t('a resume refusal that answers after the key was dropped and entered again leaves the new key alone',async()=>{
 const paused=task({status:'paused',failed_count:1,instances:[inst('val-a',[node('node',0,{status:'failed',exit_code:3})],{status:'failed'})]});
 const env=await setup({detail:paused,gated:true});const {w,d}=env;try{
 let refuse=false;
 w.__routes.unshift([new RegExp(DETAIL_URL+'$'),(u,o,j)=>{env.details.push({headers:o.headers||{}});
  if(refuse||(o.headers||{})['X-Blacksmith-Command-Key']!==KEY) return j(401,{success:false,error:'k',code:'command_credential_required'});
  return j(200,env.state.detail);}]);
 await w.__T.selectUpgradeTask(TASK);await sleep(10);
 await unlock(d);
 let release;const gate=new Promise((r)=>{release=r;});
 w.__routes.unshift([`${DETAIL_URL}/resume`,async(u,o,j)=>{env.resumes.push({headers:o.headers||{}});await gate;return j(401,{success:false,error:'k',code:'command_credential_required'});}]);
 press(d,'.resume-btn');await sleep(10);
 expect(env.resumes[0].headers['X-Blacksmith-Command-Key']===KEY).toBe(true);
 // The poll meets a refusal, so the key goes. The operator enters it again.
 refuse=true;
 env.state.detail={...paused,status:'running'};
 await w.__T.fetchUpgradeTasks();await sleep(30);
 expect(panel(d).textContent.includes('did not accept')).toBe(true);
 refuse=false;
 await unlock(d);
 expect(panel(d).querySelector('pre.command-text')!==null).toBe(true);
 release();await sleep(40);
 expect(panel(d).querySelector('pre.command-text')!==null).toBe(true);
 expect(panel(d).textContent.includes('did not accept')).toBe(false);
 await w.__T.selectUpgradeTask(TASK);await sleep(10);
 expect(env.details[env.details.length-1].headers['X-Blacksmith-Command-Key']===KEY).toBe(true);
 }finally{w.close();}
});

// A response whose headers are here but whose body waits for the test.
const heldBody=(status,make)=>{let release;const gate=new Promise((r)=>{release=r;});
 const reply={ok:status>=200&&status<300,status,statusText:'st',json:()=>gate.then(make),text:()=>gate.then(()=>JSON.stringify(make()))};
 return {reply,release:()=>release()};};

t('a detail body that arrives after the key was dropped paints and caches nothing, for select and poll',async()=>{
 for(const via of ['select','poll']){
  const env=await setup({gated:true});const {w,d}=env;try{
  await w.__T.selectUpgradeTask(TASK);await sleep(10);
  await unlock(d);
  const held=heldBody(200,()=>task({status:'running'}));
  w.__routes.unshift([new RegExp(DETAIL_URL+'$'),(u,o,j)=>{env.details.push({headers:o.headers||{}});return Promise.resolve(held.reply);}]);
  env.state.detail=task({status:'running'});
  const pending=via==='select'?w.__T.selectUpgradeTask(TASK):w.__T.fetchUpgradeTasks();
  await sleep(10);
  expect(env.details[env.details.length-1].headers['X-Blacksmith-Command-Key']===KEY).toBe(true);
  press(d,'.tab-button[data-tab="services"]');await sleep(20);
  held.release();
  const result=await pending;await sleep(30);
  if(via==='poll') expect(result).toBe(true);
  expect(hasSecret(d.body.textContent)).toBe(false);
  expect(w.__T.taskDetailCache.get(TASK)===undefined).toBe(true);
  expect(noKeyIn(d)).toBe(true);
  // Coming back, nothing is shown and no key is sent.
  const seen=env.details.length;
  press(d,'.tab-button[data-tab="upgrade"]');await sleep(60);
  expect(hasSecret(d.body.textContent)).toBe(false);
  expect(env.details.slice(seen).every(r=>r.headers['X-Blacksmith-Command-Key']===undefined)).toBe(true);
  }finally{w.close();}
 }
});

t('a refusal for a command task the list never showed asks plainly and keeps the held key',async()=>{
 const NEW='44444444-4444-4444-8444-444444444444';
 const env=await setup({gated:true});const {w,d}=env;try{
 await w.__T.selectUpgradeTask(TASK);await sleep(10);
 await unlock(d);
 const reads=[];
 w.__routes.unshift([new RegExp(NEW+'$'),(u,o,j)=>{reads.push({headers:o.headers||{}});return j(401,{success:false,error:'k',code:'command_credential_required'});}]);
 await w.__T.selectUpgradeTask(NEW);await sleep(20);
 expect(reads.length).toBe(1);
 expect(reads[0].headers['X-Blacksmith-Command-Key']===undefined).toBe(true);
 expect(panel(d).textContent.includes('did not accept')).toBe(false);
 expect(panel(d).textContent.includes('asks for a command key')).toBe(true);
 await w.__T.selectUpgradeTask(TASK);await sleep(20);
 expect(env.details[env.details.length-1].headers['X-Blacksmith-Command-Key']===KEY).toBe(true);
 expect(panel(d).querySelector('pre.command-text')!==null).toBe(true);
 }finally{w.close();}
});

t('an output refusal for a task the operator left drops the key but draws no rejection over the open task',async()=>{
 const B='55555555-5555-4555-8555-555555555555';
 const env=await setup({gated:true,output:(u,o)=>(o.headers&&o.headers['X-Blacksmith-Command-Key']===KEY)?[200,output()]:[401,{success:false,error:'k',code:'command_credential_required'}]});const {w,d}=env;try{
 await w.__T.selectUpgradeTask(TASK);await sleep(10);
 await unlock(d);
 let release;const gate=new Promise((r)=>{release=r;});
 w.__routes.unshift([OUT_URL,async(u,o,j)=>{env.outputs.push({url:u,headers:o.headers||{}});await gate;return j(401,{success:false,error:'k',code:'command_credential_required'});}]);
 await openDetails(env,0);
 w.__routes.unshift([new RegExp(B+'$'),(u,o,j)=>j(200,task({id:B}))]);
 await w.__T.selectUpgradeTask(B);await sleep(20);
 expect(w.__T._getSelectedTask()===B).toBe(true);
 release();await sleep(40);
 expect(panel(d).textContent.includes('did not accept')).toBe(false);
 expect(hasSecret(d.body.textContent)).toBe(false);
 expect(noKeyIn(d)).toBe(true);
 // The key is gone, so the first task asks for it again.
 await w.__T.selectUpgradeTask(TASK);await sleep(20);
 expect(env.details[env.details.length-1].headers['X-Blacksmith-Command-Key']===undefined).toBe(true);
 }finally{w.close();}
});

t('an output answer for a task the operator left is not kept, so the node reads it again',async()=>{
 const B='66666666-6666-4666-8666-666666666666';
 const env=await setup();const {w,d}=env;try{
 await open(env);
 let release;const gate=new Promise((r)=>{release=r;});
 w.__routes.unshift([OUT_URL,async(u,o,j)=>{env.outputs.push({url:u,headers:o.headers||{}});if(env.outputs.length===1) await gate;return j(200,output());}]);
 await openDetails(env,0);
 expect(env.outputs.length).toBe(1);
 w.__routes.unshift([new RegExp(B+'$'),(u,o,j)=>j(200,task({id:B}))]);
 await w.__T.selectUpgradeTask(B);await sleep(20);
 release();await sleep(30);
 await w.__T.selectUpgradeTask(TASK);await sleep(20);
 await openDetails(env,0);
 expect(env.outputs.length).toBe(2);
 }finally{w.close();}
});

t('the resume key prompt goes away once the task is no longer paused',async()=>{
 const paused=task({status:'paused',failed_count:1,instances:[inst('val-a',[node('node',0,{status:'failed',exit_code:3})],{status:'failed'})]});
 const env=await setup({detail:paused,gated:true});const {w,d}=env;try{
 w.__routes.unshift([new RegExp(DETAIL_URL+'$'),(u,o,j)=>{env.details.push({headers:o.headers||{}});return j(200,env.state.detail);}]);
 await w.__T.selectUpgradeTask(TASK);await sleep(10);
 press(d,'.resume-btn');await sleep(30);
 expect(panel(d).querySelector('.command-key-prompt')!==null).toBe(true);
 env.state.detail=task({status:'running'});
 await w.__T.fetchUpgradeTasks();await sleep(30);
 expect(panel(d).querySelector('pre.command-text')!==null).toBe(true);
 expect(panel(d).querySelector('.command-key-prompt')===null).toBe(true);
 }finally{w.close();}
});

t('a settled node with no kept output says so, reads nothing, and offers no Retry',async()=>{
 const gone=(group,index,over={})=>node(group,index,{status:'failed',exit_code:undefined,output_path:undefined,stdout_kept:0,stdout_received:0,reason:'the node moved to another VM',...over});
 const env=await setup({detail:task({instances:[inst('val-a',[gone('node',0),gone('node',1,{reason:undefined})])]})});const {w,d}=env;try{
 await open(env);
 const first=detailsOf(d)[0];
 expect(first.querySelector('summary').textContent).toBe('Output (none kept)');
 await openDetails(env,0);await openDetails(env,1);
 expect(env.outputs.length).toBe(0);
 const body=detailsOf(d)[0].querySelector('.command-output-body');
 expect(body.textContent.includes('produced no output')).toBe(true);
 expect(body.textContent.includes('the node moved to another VM')).toBe(true);
 expect(detailsOf(d)[1].querySelector('.command-output-body').textContent.includes('produced no output')).toBe(true);
 expect(detailsOf(d).every(el=>el.querySelector('button')===null)).toBe(true);
 expect(pageText(d).includes('Could not read the output')).toBe(false);
 }finally{w.close();}
});

// Each of these can disguise a command without drawing anything, so the
// review writes it as a visible escape and says so.
for(const code of [0x061C,0x200B,0x200C,0x200D,0x2060,0xFEFF,0x00AD]){
 const hex=code.toString(16).toUpperCase().padStart(4,'0');
 t(`the review shows U+${hex} as an escape and notes the hidden character`,async()=>{
  const env=await setup();const {w}=env;try{
  const text='echo a'+String.fromCharCode(code)+'b';
  const review=w.__T.buildCommandReviewBody({instances:[],warnings:[],run_as:'vcap',shell:'/bin/bash',timeout_seconds:600},{command:text,run_as:'vcap',timeout_seconds:600,parallel:false});
  expect(review.querySelector('pre').textContent==='echo a\\u'+hex+'b').toBe(true);
  expect(review.querySelector('.command-hidden-note')!==null).toBe(true);
  expect(review.textContent.includes(String.fromCharCode(code))).toBe(false);
  }finally{w.close();}
 });
}

t('with a key configured and none held, holder reads stop asking for command task detail',async()=>{
 const env=await setup({gated:true,detail:task({status:'running'})});const {w,d}=env;try{
 press(d,'.tab-button[data-tab="upgrade"]');await sleep(30);
 // The first read learns that the broker wants a key. Reads after it send nothing.
 await w.__T.fetchUpgradeTasks();await sleep(40);
 const learned=env.details.length;
 expect(learned>0).toBe(true);
 expect(env.details.every(r=>r.headers['X-Blacksmith-Command-Key']===undefined)).toBe(true);
 for(let i=0;i<3;i++){await w.__T.fetchUpgradeTasks();await sleep(20);}
 await w.blacksmithRotate.readHolders();
 expect(env.details.length).toBe(learned);
 // A held key brings the reads back, and they carry it.
 await w.__T.selectUpgradeTask(TASK);await sleep(10);
 const before=env.details.length;
 await unlock(d);
 await w.__T.fetchUpgradeTasks();await sleep(40);
 expect(env.details.length>before).toBe(true);
 expect(env.details.slice(before).every(r=>r.headers['X-Blacksmith-Command-Key']===KEY)).toBe(true);
 }finally{w.close();}
});

t('a broker without a command key keeps serving holder reads to a page with none',async()=>{
 const env=await setup({detail:task({status:'running'})});const {w,d}=env;try{
 press(d,'.tab-button[data-tab="upgrade"]');await sleep(30);
 await w.__T.fetchUpgradeTasks();await sleep(40);
 const first=env.details.length;
 expect(first>0).toBe(true);
 await w.blacksmithRotate.readHolders();
 expect(env.details.length>first).toBe(true);
 }finally{w.close();}
});

t('an instance held by a command task still shows as held when its detail is out of reach',async()=>{
 const env=await setup({gated:true,detail:task({status:'running'})});const {w,d}=env;try{
 press(d,'.tab-button[data-tab="upgrade"]');await sleep(30);
 await w.__T.fetchUpgradeTasks();await sleep(40);
 const learned=env.details.length;
 // A list entry that names its instances holds them without a detail read.
 const listed={id:TASK,kind:'command',status:'running',name:'restart check',instances:[{instance_id:'val-a',status:'running'}]};
 w.__routes.unshift(['/b/upgrade/tasks',(u,o,j)=>j(200,[listed])]);
 await w.__T.fetchUpgradeTasks();await sleep(40);
 expect(env.details.length).toBe(learned);
 const kept=w.__T.holderDetails.get(TASK);
 expect(kept!==undefined&&kept.detail.instances.map(i=>i.instance_id).join()==='val-a').toBe(true);
 const holders=await w.blacksmithRotate.readHolders();
 expect(env.details.length).toBe(learned);
 expect(holders.get('val-a')).toBe('command run "restart check"');
 }finally{w.close();}
});

t('an output body that arrives after the key was dropped paints nothing',async()=>{
 const env=await setup({gated:true,output:(u,o)=>(o.headers&&o.headers['X-Blacksmith-Command-Key']===KEY)?[200,output()]:[401,{success:false,error:'k',code:'command_credential_required'}]});const {w,d}=env;try{
 await w.__T.selectUpgradeTask(TASK);await sleep(10);
 await unlock(d);
 // Only the body waits, so the answer is past the generation check at the headers.
 const held=heldBody(200,()=>output({stdout:'LATE-BODY-OUT\n'}));
 w.__routes.unshift([OUT_URL,(u,o,j)=>{env.outputs.push({url:u,headers:o.headers||{}});return Promise.resolve(held.reply);}]);
 await openDetails(env,0);
 expect(env.outputs.length).toBe(1);
 expect(env.outputs[0].headers['X-Blacksmith-Command-Key']===KEY).toBe(true);
 // The broker refuses the held key on a poll, which drops it while the same task stays on screen.
 w.__routes.unshift([new RegExp(DETAIL_URL+'$'),(u,o,j)=>{env.details.push({headers:o.headers||{}});return j(401,{success:false,error:'k',code:'command_credential_required'});}]);
 env.state.detail=task({status:'running'});
 await w.__T.fetchUpgradeTasks();await sleep(30);
 expect(panel(d).textContent.includes('did not accept')).toBe(true);
 held.release();await sleep(40);
 expect(d.body.textContent.includes('LATE-BODY-OUT')).toBe(false);
 expect(noKeyIn(d)).toBe(true);
 // With the key entered again the output is read afresh, so the late body left nothing behind.
 w.__routes.shift();
 w.__routes.unshift([OUT_URL,(u,o,j)=>{env.outputs.push({url:u,headers:o.headers||{}});return j(200,output());}]);
 await unlock(d);
 await openDetails(env,0);
 expect(env.outputs.length).toBe(2);
 expect(d.body.textContent.includes('hello out')).toBe(true);
 expect(d.body.textContent.includes('LATE-BODY-OUT')).toBe(false);
 }finally{w.close();}
});
