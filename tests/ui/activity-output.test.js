const {test,expect,afterEach}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const SECRET='echo SECRET-COMMAND-TEXT-9921';
const OUTPUT='SECRET-OUTPUT-TEXT-4417';
const TASK='22222222-2222-4222-8222-222222222222';
const OUT_URL=`/b/upgrade/tasks/${TASK}/output`;
const DETAIL_URL=`/b/upgrade/tasks/${TASK}`;

const envs=[];
afterEach(()=>{ while(envs.length) envs.pop().w.close(); });

const when=(s)=>new Date(Date.UTC(2026,9,26,12,0,0)+s*1000).toISOString();
const node=(group,index,over={})=>({reviewed:{group,index,id:'vm-'+index,cid:'c'+index},matched:{group,index,id:'vm-'+index,cid:'c'+index},phase:'settled',status:'succeeded',exit_code:0,started_at:when(0),ended_at:when(5),stdout_kept:20,stdout_received:20,stderr_kept:0,stderr_received:0,output_path:'command-output/x',...over});
const task=(over={},vms=[node('node',0)])=>({id:TASK,kind:'command',status:'completed',name:'Run command',total_count:1,completed_count:1,failed_count:0,created_at:when(0),
 command:{command:SECRET,run_as:'vcap',shell:'/bin/bash',timeout_seconds:600,parallel:false,max_in_flight:1},
 instances:[{instance_id:'val-a',instance_name:'val-a-name',deployment_name:'dep-val-a',status:'success',command:{vms}}],...over});
const legacy=(over={})=>({stdout:'hello out\n',stderr:'oops err\n',stdout_kept:10,stdout_received:10,stderr_kept:9,stderr_received:9,stdout_truncated:false,stderr_truncated:false,...over});
const merged=(over={})=>({merged:true,stdout:`${OUTPUT}\nsecond\n`,stderr:'',stdout_kept:20,stdout_received:20,stderr_kept:0,stderr_received:0,stdout_truncated:false,stderr_truncated:false,...over});

async function setup(detail,answer){
 const env=boot({noDialogs:true});const {w}=env;envs.push(env);
 const copied=[];
 w.navigator.clipboard={writeText:(x)=>{copied.push(x);return Promise.resolve();}};
 w.__routes.push(['/b/bosh/stemcells',(u,o,j)=>j(200,[])],['/b/bosh/releases',(u,o,j)=>j(200,[])],['/b/bosh/deployment-releases',(u,o,j)=>j(200,{})],['/b/upgrade/settings',(u,o,j)=>j(200,{})],
  [OUT_URL,(u,o,j)=>j(200,answer)],[DETAIL_URL,(u,o,j)=>j(200,detail)],['/b/upgrade/tasks',(u,o,j)=>j(200,[])]);
 w.plansData={services:[]};
 await sleep(30);
 await w.__T.initUpgradeTab();await sleep(10);
 await w.__T.selectUpgradeTask(TASK);await sleep(10);
 return {...env,copied};
}
const details=(d)=>d.querySelector('#upgrade .command-output-details');
const outCopy=(d)=>d.querySelector('#upgrade button.command-output-copy');
const openOut=async(d)=>{const el=details(d);el.open=true;el.dispatchEvent(new d.defaultView.Event('toggle'));await sleep(40);};

t('the command sits in a block below the table with an icon-only copy button',async()=>{
 const {d,copied}=await setup(task(),merged());
 const block=d.querySelector('#upgrade .command-block');
 expect(block).not.toBeNull();
 expect(d.querySelector('#upgrade .task-summary-table').compareDocumentPosition(block)&4).toBeTruthy();
 expect(block.querySelector('pre.command-text').textContent).toBe(SECRET);
 const button=block.querySelector('button.command-copy');
 expect(button.getAttribute('aria-label')).toBe('Copy command');
 expect(button.textContent.trim()).toBe('');
 expect(button.querySelector('svg')).not.toBeNull();
 expect(button.dataset.keepKey).toBe('command-copy');
 expect([...button.attributes].some((a)=>a.value.includes('SECRET'))).toBe(false);
 button.click();await sleep(10);
 expect(copied).toEqual([SECRET]);
 expect(button.classList.contains('copied')).toBe(true);
});

t('the Output copy button sits outside the summary, shows once output loads, and copies the displayed text',async()=>{
 const {d,copied}=await setup(task(),merged({stdout:`${OUTPUT}\u0007\n`,stdout_received:25}));
 const block=d.querySelector('#upgrade .command-output-block');
 expect(block).not.toBeNull();
 expect(outCopy(d).hidden).toBe(true);
 expect(details(d).querySelector('summary button')).toBeNull();
 expect(block.contains(outCopy(d))).toBe(true);
 expect(details(d).contains(outCopy(d))).toBe(false);
 await openOut(d);
 const button=outCopy(d);
 expect(button.hidden).toBe(false);
 expect(button.getAttribute('aria-label')).toBe('Copy output');
 expect(button.textContent.trim()).toBe('');
 expect(button.dataset.keepKey).toMatch(/^output-copy:/);
 expect([...d.querySelectorAll('*')].some((e)=>e.id!=='x'&&[...e.attributes].some((a)=>a.value.includes(OUTPUT)))).toBe(false);
 button.click();await sleep(10);
 expect(copied).toEqual([`${OUTPUT}\n`]);
 expect(details(d).open).toBe(true);
});

t('merged output is one block with no stream labels',async()=>{
 const {d}=await setup(task({},[node('node',0,{merged:true})]),merged());
 await openOut(d);
 const pres=[...details(d).querySelectorAll('pre')];
 expect(pres.length).toBe(1);
 expect(pres[0].classList.contains('command-output-merged')).toBe(true);
 expect(pres[0].textContent).toBe(`${OUTPUT}\nsecond\n`);
 expect(details(d).querySelector('.command-output-label')).toBeNull();
 expect(details(d).textContent).not.toContain('stdout');
 expect(details(d).querySelector('summary').textContent).not.toMatch(/stdout|stderr/);
});

t('an older record shows stdout, a captioned stderr, and the ordering note',async()=>{
 const {d,copied}=await setup(task(),legacy());
 await openOut(d);
 const body=details(d).querySelector('.command-output-body');
 const labels=[...body.querySelectorAll('.command-output-label')].map((x)=>x.textContent);
 expect(labels).toEqual(['stderr']);
 expect(body.textContent).toContain('This run stored stdout and stderr separately, so their original order is unknown.');
 const pres=[...body.querySelectorAll('pre')].map((p)=>p.textContent);
 expect(pres).toEqual(['hello out\n','oops err\n']);
 outCopy(d).click();await sleep(10);
 expect(copied).toEqual(['hello out\noops err\n']);
});

t('an older record with one stream shows no ordering note',async()=>{
 const {d}=await setup(task(),legacy({stderr:'',stderr_kept:0,stderr_received:0}));
 await openOut(d);
 expect(details(d).textContent).not.toContain('original order is unknown');
 expect(details(d).querySelector('.command-output-label')).toBeNull();
});

t('a poll re-render keeps focus on the focused copy buttons',async()=>{
 const {w,d}=await setup(task(),merged());
 await openOut(d);
 for(const sel of ['#upgrade button.command-copy','#upgrade button.command-output-copy']){
  const before=d.querySelector(sel);
  before.focus();
  expect(d.activeElement).toBe(before);
  w.__T.renderTaskDetail(task({status:'running'}));
  const after=d.querySelector(sel);
  expect(after).not.toBe(before);
  expect(d.activeElement).toBe(after);
 }
});

t('every new control has a distinct keep key',async()=>{
 const {d}=await setup(task({},[node('node',0),node('node',1)]),merged());
 const keys=[...d.querySelectorAll('#upgrade [data-keep-key]')].map((e)=>e.dataset.keepKey);
 expect(new Set(keys).size).toBe(keys.length);
});
