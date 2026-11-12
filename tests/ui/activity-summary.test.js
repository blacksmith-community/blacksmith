const {test,expect,afterEach}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const {watchUncaught}=require('./watch-uncaught.js');
const t=JSDOM?test:test.skip;

const UI=path.resolve(__dirname,'..','..','ui');
const envs=[];
afterEach(()=>{ while(envs.length) envs.pop().w.close(); });

const when=(s)=>new Date(Date.UTC(2026,9,26,12,0,0)+s*1000).toISOString();
const node=(group,index,over={})=>({reviewed:{group,index,id:'vm-'+index,cid:'c'+index},matched:{group,index,id:'vm-'+index,cid:'c'+index},phase:'settled',status:'succeeded',exit_code:0,started_at:when(0),ended_at:when(5),stdout_kept:3,stdout_received:3,stderr_kept:0,stderr_received:0,output_path:'command-output/x',...over});
const commandTask=(over={})=>({id:'cmd-1',kind:'command',status:'running',name:'Run command on 3 nodes',total_count:1,completed_count:0,failed_count:0,created_at:when(0),
 command:{command:'uptime',run_as:'vcap',shell:'/bin/bash',timeout_seconds:600,parallel:true,max_in_flight:2,reason:'check'},
 instances:[{instance_id:'val-a',instance_name:'val-a-name',deployment_name:'dep-val-a',status:'running',command:{vms:[node('node',0),node('node',1),node('node',2,{phase:'sent',status:''})]}}],...over});
const upgradeTask=(over={})=>({id:'up-1',kind:'upgrade',status:'paused',pause_on_failure:true,pause_reason:'Instance a failed, so the job paused.',target_stemcell:'ubuntu-noble/1.1',total_count:2,completed_count:1,failed_count:1,created_at:when(0),
 instances:[{instance_id:'inst-a',instance_name:'cache-a',status:'failed'},{instance_id:'inst-b',instance_name:'cache-b',status:'pending'}],...over});
const certTask=(over={})=>({id:'cert-1',kind:'certificates',status:'completed',certificates:{mode:'redeploy',reason:'rotate'},total_count:1,completed_count:1,failed_count:0,created_at:when(0),
 instances:[{instance_id:'i1',instance_name:'blacksmith-0',deployment_name:'dep-one',status:'success',certificates:{phase:'done',variables:[]}}],...over});

const start=async(task)=>{
 const env=boot({noDialogs:true});envs.push(env);
 await sleep(60);
 env.w.__T.renderTaskDetail(task);
 return env;
};
const headers=(d)=>[...d.querySelectorAll('#upgrade .task-summary-table thead th')].map((x)=>x.textContent);
const cells=(d)=>Object.fromEntries([...d.querySelectorAll('#upgrade .task-summary-table tbody td')].map((x)=>[x.dataset.label,x.textContent]));

t('a command run summary is one data row with Status first and Created last',async()=>{
 const {d}=await start(commandTask());
 const table=d.querySelector('#upgrade .task-detail-summary table.task-summary-table');
 expect(table).not.toBeNull();
 expect(table.querySelectorAll('tbody tr').length).toBe(1);
 expect(table.getAttribute('data-sortable')).toBe('false');
 expect(headers(d)).toEqual(['Status','Run as','Shell','Timeout','Parallel','Reason','Progress','Instances','Created']);
 expect(d.querySelector('#upgrade .task-detail-summary .summary-row')).toBeNull();
});

t('an upgrade and a certificate job use the same table with their own settings',async()=>{
 const up=await start(upgradeTask());
 expect(headers(up.d)).toEqual(['Status','System','After a failure','Progress','Instances','Created']);
 const cert=await start(certTask());
 expect(headers(cert.d)).toEqual(['Status','Mode','Reason','After a failure','Progress','Instances','Created']);
});

t('Instances is a count in its own cell and the old heading is gone',async()=>{
 const {d}=await start(upgradeTask());
 expect(cells(d).Instances).toBe('2');
 expect([...d.querySelectorAll('#upgrade .upgrade-task-detail h4')].some((h)=>/^Instances/.test(h.textContent))).toBe(false);
});

t('progress names its unit and a command run adds node progress',async()=>{
 const up=await start(upgradeTask());
 expect(cells(up.d).Progress).toBe('1/2 instances completed, 1 failed (100%)');
 const cmd=await start(commandTask());
 expect(cells(cmd.d).Progress).toContain('instances completed');
 expect(cells(cmd.d).Progress).toContain('2/3 nodes');
});

t('failure guidance stays as notices below the table',async()=>{
 const {d}=await start(upgradeTask());
 const box=d.querySelector('#upgrade .task-detail-summary');
 const notices=[...box.querySelectorAll(':scope > .task-notice')];
 expect(notices.length).toBe(2);
 expect(notices[0].querySelector('.task-pause-reason').textContent).toBe('Instance a failed, so the job paused.');
 expect(notices[1].textContent).toContain('never retries the failed instance');
 expect(box.querySelector('table').compareDocumentPosition(notices[0])&4).toBeTruthy();
 expect(d.querySelector('#upgrade .task-summary-table .task-pause-reason')).toBeNull();
});

t('the Back to Jobs button is gone and the title shares a row with the controls',async()=>{
 const {d}=await start(commandTask());
 expect(d.querySelector('#upgrade .back-to-list')).toBeNull();
 expect(d.body.textContent).not.toContain('Back to Jobs');
 const head=d.querySelector('#upgrade .task-detail-header');
 expect(head.querySelector('h3')).not.toBeNull();
 expect(head.querySelector('.job-control-buttons')).not.toBeNull();
 expect([...head.children].map((c)=>c.tagName.toLowerCase())).toEqual(['h3','div']);
});

t('the summary stacks as labelled pairs on a phone',()=>{
 const css=fs.readFileSync(path.join(UI,'blacksmith.css'),'utf8');
 const phone=css.slice(css.indexOf('/* Activity summary table */'));
 expect(phone).toMatch(/@media\s*\(max-width:\s*\d+px\)\s*\{[\s\S]*?\.task-summary-table td::before\s*\{[^}]*attr\(data-label\)/);
});

const crumbs=async(hash,stamp)=>{
 const dom=new JSDOM('<main id="console-main"><h1 id="console-title">Blacksmith</h1><div class="tab-content"></div></main>',{url:'http://localhost/'+hash,runScripts:'outside-only',pretendToBeVisual:true});
 const w=watchUncaught(dom.window);
 w.blacksmithViews={activate:async (r)=>r};
 w.blacksmithPollStamp=()=>stamp;
 w.eval(fs.readFileSync(path.join(UI,'routes.js'),'utf8'));
 await sleep(40);
 return w;
};

t('the Updated stamp ends the breadcrumb bar on a job page and nowhere else',async()=>{
 const job=await crumbs('#/batch/activity/job-1','Updated 2026-10-26 12:00:00 UTC');
 const bar=job.document.getElementById('route-context');
 expect(bar.lastElementChild.id).toBe('route-updated');
 expect(bar.lastElementChild.textContent).toBe('Updated 2026-10-26 12:00:00 UTC');
 job.blacksmithRouter.destroy();job.close();
 const list=await crumbs('#/batch/activity','Updated now');
 expect(list.document.getElementById('route-updated')).toBeNull();
 list.blacksmithRouter.destroy();list.close();
});

t('a poll moves the stamp in the route bar and hides the old line on a job page',async()=>{
 const env=await start(commandTask());
 const {w,d}=env;
 const bar=d.createElement('div');bar.id='route-context';
 const stamp=d.createElement('span');stamp.id='route-updated';bar.append(stamp);
 d.body.prepend(bar);
 w.__routes.push(['/b/upgrade/tasks',(u,o,j)=>j(200,[])]);
 await w.__T.fetchUpgradeTasks();
 expect(stamp.textContent).toMatch(/^Updated \d{4}-\d\d-\d\d \d\d:\d\d:\d\d [A-Z]+$/);
 const css=fs.readFileSync(path.join(UI,'blacksmith.css'),'utf8');
 expect(css).toMatch(/#upgrade \.upgrade-right-panel\.job-detail \.upgrade-poll-updated\s*\{\s*display:\s*none/);
});
