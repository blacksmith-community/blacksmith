const {test,expect,afterEach}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const TASK='33333333-3333-4333-8333-333333333333';
const OUT_URL=`/b/upgrade/tasks/${TASK}/output`;
const DETAIL_URL=`/b/upgrade/tasks/${TASK}`;
const SKIP_REASON='An earlier node of the deployment did not succeed, so the command was not sent.';
const DENIED="ls: cannot open directory '/var/vcap/data/x': Permission denied";

const envs=[];
afterEach(()=>{ while(envs.length) envs.pop().w.close(); });

const when=(s)=>new Date(Date.UTC(2026,9,26,12,0,0)+s*1000).toISOString();
const node=(group,index,over={})=>({reviewed:{group,index,id:'vm-'+index,cid:'c'+index},matched:{group,index,id:'vm-'+index,cid:'c'+index},phase:'settled',status:'succeeded',exit_code:0,started_at:when(0),ended_at:when(5),stdout_kept:20,stdout_received:20,stderr_kept:0,stderr_received:0,output_path:'command-output/x',...over});
const failedNode=(over={})=>node('node',0,{status:'failed',exit_code:2,ended_at:when(0.2),reason:'The command exited with status 2.',...over});
const skipped=(i)=>node('node',i,{phase:'skipped',status:'',exit_code:undefined,started_at:undefined,ended_at:undefined,output_path:'',reason:SKIP_REASON});
const task=(vms,over={},spec={})=>({id:TASK,kind:'command',status:'failed',name:'Run command on 6 nodes',total_count:1,completed_count:0,failed_count:1,created_at:when(0),
 command:{command:'ls /var/vcap/data',run_as:'vcap',shell:'/bin/bash',timeout_seconds:600,parallel:false,max_in_flight:1,...spec},
 instances:[{instance_id:'val-a',instance_name:'certmgmt-valkey-tls',deployment_name:'dep-val-a',status:'failed',error:'The command exited with status 2.',command:{vms}}],...over});
const wayne=()=>task([failedNode(),...[1,2,3,4,5].map(skipped)]);
const merged=(stdout)=>({merged:true,stdout,stderr:'',stdout_kept:stdout.length,stdout_received:stdout.length,stderr_kept:0,stderr_received:0,stdout_truncated:false,stderr_truncated:false});
const nine=()=>Array.from({length:9},(_,i)=>`${DENIED.replace('x','d'+i)}`).join('\n')+'\n';

async function setup(detail,answer){
 const env=boot({noDialogs:true});const {w}=env;envs.push(env);
 w.navigator.clipboard={writeText:()=>Promise.resolve()};
 w.__routes.push(['/b/bosh/stemcells',(u,o,j)=>j(200,[])],['/b/bosh/releases',(u,o,j)=>j(200,[])],['/b/bosh/deployment-releases',(u,o,j)=>j(200,{})],['/b/upgrade/settings',(u,o,j)=>j(200,{})],
  [OUT_URL,(u,o,j)=>j(200,answer)],[DETAIL_URL,(u,o,j)=>j(200,detail)],['/b/upgrade/tasks',(u,o,j)=>j(200,[])]);
 w.plansData={services:[]};
 await sleep(30);
 await w.__T.initUpgradeTab();await sleep(10);
 await w.__T.selectUpgradeTask(TASK);await sleep(80);
 return env;
}
const explain=(d)=>d.querySelector('#upgrade .command-node-explain');
const text=(el)=>(el?el.textContent:'').replace(/\s+/g,' ').trim();

t('a failed node opens its output on its own and loads it',async()=>{
 const {d}=await setup(wayne(),merged(nine()));
 const toggle=d.querySelector('#upgrade .command-node .command-output-toggle');
 expect(toggle.getAttribute('aria-expanded')).toBe('true');
 expect(d.querySelector('#upgrade .command-output-body').hidden).toBe(false);
 expect(d.querySelector('#upgrade .command-output-body pre').textContent).toContain('Permission denied');
});

t('a node that succeeded keeps its output closed',async()=>{
 const ok=task([node('node',0)],{status:'completed',failed_count:0,completed_count:1});
 ok.instances[0].status='success';ok.instances[0].error='';
 const {d}=await setup(ok,merged('ok\n'));
 expect(d.querySelector('#upgrade .command-output-toggle').getAttribute('aria-expanded')).toBe('false');
 expect(d.querySelector('#upgrade .command-node-explain')).toBeNull();
});

t('the first line gives the status, the run time, and the user',async()=>{
 const {d}=await setup(wayne(),merged(nine()));
 expect(text(d.querySelector('#upgrade .command-node-explain-summary'))).toBe('Exited with status 2 after 0.2s, running as vcap.');
});

t('at most three error-looking lines show, with a count when there are more',async()=>{
 const {d}=await setup(wayne(),merged(`start\n${nine()}done\n`));
 const items=[...d.querySelectorAll('#upgrade .command-node-explain li')];
 expect(items.length).toBe(3);
 expect(items[0].textContent).toBe(DENIED.replace('x','d0'));
 expect(text(explain(d))).toContain('9 lines in the output report errors.');
});

t('an error line is cut near 200 characters and loses control bytes',async()=>{
 const long='error: '+'x'.repeat(400);
 const {d}=await setup(wayne(),merged(`bad\u0007 thing: Permission denied\n${long}\n`));
 const items=[...d.querySelectorAll('#upgrade .command-node-explain li')].map((li)=>li.textContent);
 expect(items[0]).toBe('bad thing: Permission denied');
 expect(items[1].length).toBeLessThanOrEqual(201);
 expect(items[1].startsWith('error: xxx')).toBe(true);
 expect(text(explain(d))).not.toMatch(/lines in the output report/);
});

t('a few matching lines show without a count',async()=>{
 const {d}=await setup(wayne(),merged('one: no such file\ntwo\n'));
 expect(d.querySelectorAll('#upgrade .command-node-explain li').length).toBe(1);
 expect(text(explain(d))).not.toMatch(/report errors/);
});

t('a run as vcap with Permission denied says to pick root',async()=>{
 const {d}=await setup(wayne(),merged(nine()));
 expect(text(explain(d))).toContain('It ran as vcap, so choose root under Run as if it needs root access.');
 const root=await setup(task([failedNode(),skipped(1)],{},{run_as:'root'}),merged(nine()));
 expect(text(explain(root.d))).not.toMatch(/choose root/);
});

t('well-known exit codes get a hint',async()=>{
 const cases=[[127,/command not found/i],[126,/not executable/i],[130,/interrupted/i],[137,/killed.*memory/i],[124,/timed out/i]];
 for(const [code,re] of cases){
  const {d}=await setup(task([failedNode({exit_code:code,reason:`The command exited with status ${code}.`})]),merged('boom\n'));
  expect(text(explain(d))).toMatch(re);
 }
 const timed=await setup(task([failedNode({status:'timed_out',exit_code:undefined,reason:'The command ran past its timeout of 600 seconds.'})]),merged('boom\n'));
 expect(text(explain(timed.d))).toMatch(/timed out/i);
});

t('a record with separate stderr takes the excerpt from stderr first',async()=>{
 const {d}=await setup(wayne(),{stdout:'error: from stdout\n',stderr:'ls: x: Permission denied\n',stdout_kept:19,stdout_received:19,stderr_kept:25,stderr_received:25});
 const items=[...d.querySelectorAll('#upgrade .command-node-explain li')].map((li)=>li.textContent);
 expect(items).toEqual(['ls: x: Permission denied']);
});

t('the matching lines are tinted in the output and the copy text stays plain',async()=>{
 const copied=[];
 const env=await setup(wayne(),merged(`start\n${nine()}done\n`));
 env.w.navigator.clipboard={writeText:(x)=>{copied.push(x);return Promise.resolve();}};
 const marks=env.d.querySelectorAll('#upgrade .command-output-body .command-output-error-line');
 expect(marks.length).toBe(9);
 expect(env.d.querySelector('#upgrade .command-output-body pre').textContent).toBe(`start\n${nine()}done\n`);
 env.d.querySelector('#upgrade button.command-output-copy').click();await sleep(10);
 expect(copied).toEqual([`start\n${nine()}done\n`]);
});

t('the failure is explained once, on the node, and the instance row summarises',async()=>{
 const {d}=await setup(wayne(),merged(nine()));
 expect(d.body.textContent).not.toContain('The command exited with status 2.');
 const row=d.querySelector('#upgrade .upgrade-task-instance .instance-error');
 expect(text(row)).toBe('node-0 failed (exit 2). 5 nodes were skipped.');
 expect(d.querySelectorAll('#upgrade .command-node-explain').length).toBe(1);
});

t('skipped nodes collapse into one muted line with no output panel',async()=>{
 const {d}=await setup(wayne(),merged(nine()));
 const rows=[...d.querySelectorAll('#upgrade .command-node:not(.command-node-skipped)')];
 expect(rows.length).toBe(1);
 const skip=d.querySelector('#upgrade .command-skipped');
 expect(text(skip.querySelector('.command-skipped-summary'))).toBe('node-1 to node-5 were skipped because node-0 failed.');
 expect(skip.querySelector('.command-output-block')).toBeNull();
 expect(skip.querySelectorAll('button.command-node-terminal').length).toBe(5);
 expect(skip.querySelector('.instance-error')).toBeNull();
 expect(skip.dataset.keepKey || skip.querySelector('summary').dataset.keepKey).toBeTruthy();
});

t('progress counts the nodes that ran, failed, and were skipped',async()=>{
 const {d}=await setup(wayne(),merged(nine()));
 const progress=text(d.querySelector('#upgrade .task-summary-table td[data-label="Progress"]'));
 expect(progress).toContain('0/1 instances completed, 1 failed');
 expect(progress).toContain('nodes: 1 ran, 1 failed, 5 skipped');
});

t('the error tint is built from a theme token',()=>{
 const fs=require('fs');const path=require('path');
 const css=fs.readFileSync(path.resolve(__dirname,'..','..','ui','blacksmith.css'),'utf8');
 const m=css.match(/\.command-output-error-line\s*\{([^}]*)\}/);
 expect(m).not.toBeNull();
 expect(m[1]).toMatch(/color-mix\([^)]*var\(--danger\)/);
 expect(m[1]).not.toMatch(/#[0-9a-f]{3,6}\b|rgb\(/i);
});
