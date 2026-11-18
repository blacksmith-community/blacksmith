const {test,expect,afterEach}=require('bun:test');
const {main:{boot,idle,TEST_LIMIT}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?(n,f,l)=>test(n,f,l===undefined?TEST_LIMIT:l):test.skip;

// Each Batch Activity job says who started it and where the request came
// from, in the list and on the job's own page, from the created_by and
// created_from the broker records.

const envs=[];
afterEach(()=>{ while(envs.length) envs.pop().w.close(); });

const FROM='192.0.2.10:51234 via 198.51.100.7';
const task=(over={})=>Object.assign({id:'t1',kind:'upgrade',status:'completed',target_stemcell:'ubuntu-noble/1.1',total_count:1,completed_count:1,failed_count:0,created_at:new Date(Date.UTC(2026,10,14,9,0,0)).toISOString(),created_by:'operator',created_from:FROM,
 instances:[{instance_id:'inst-a',instance_name:'cache-a',status:'success'}]},over);

const list=async(tasks)=>{
 const env=boot({noDialogs:true});envs.push(env);await idle(env.w);
 env.w.__T._setTasks(tasks);env.w.__T.renderUpgradeTaskList();
 return env;
};
const detail=async(one)=>{
 const env=boot({noDialogs:true});envs.push(env);await idle(env.w);
 env.w.__T.renderTaskDetail(one);
 return env;
};
const cells=(d)=>Object.fromEntries([...d.querySelectorAll('#upgrade .task-summary-table tbody td')].map((x)=>[x.dataset.label,x.textContent]));

t('a job in the list names who started it and the client address',async()=>{
 const {d}=await list([task()]);
 const line=d.querySelector('#upgrade .upgrade-task-item .upgrade-task-actor');
 expect(line).not.toBeNull();
 expect(line.textContent.replace(/\s+/g,' ').trim()).toBe(`Started by operator from ${FROM}`);
});

t('a job with a user but no address names the user alone',async()=>{
 const {d}=await list([task({created_from:''})]);
 expect(d.querySelector('#upgrade .upgrade-task-actor').textContent.replace(/\s+/g,' ').trim()).toBe('Started by operator');
});

t('a job the broker recorded no user for shows no actor line',async()=>{
 const {d}=await list([task({created_by:'',created_from:''})]);
 expect(d.querySelector('#upgrade .upgrade-task-actor')).toBeNull();
});

t('the job page has Started by and Client address cells before Created',async()=>{
 const {d}=await detail(task());
 const heads=[...d.querySelectorAll('#upgrade .task-summary-table thead th')].map((x)=>x.textContent);
 expect(heads.slice(-3)).toEqual(['Started by','Client address','Created']);
 expect(cells(d)['Started by']).toBe('operator');
 expect(cells(d)['Client address']).toBe(FROM);
});

t('the job page shows an empty value when nothing was recorded',async()=>{
 const {d}=await detail(task({created_by:undefined,created_from:undefined}));
 expect(cells(d)['Started by']).toBe('—');
 expect(cells(d)['Client address']).toBe('—');
});

t('a user name or address that holds markup shows as text',async()=>{
 const evil='<img src=x onerror=alert(1)>';
 const {d}=await list([task({created_by:evil,created_from:evil})]);
 expect(d.querySelector('#upgrade .upgrade-task-actor img')).toBeNull();
 expect(d.querySelector('#upgrade .upgrade-task-actor').textContent).toContain(evil);
 const page=await detail(task({created_by:evil,created_from:evil}));
 expect(page.d.querySelector('#upgrade .task-summary-table img')).toBeNull();
});
