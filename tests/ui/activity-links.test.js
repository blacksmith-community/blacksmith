const {test,expect,afterEach}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const {watchUncaught}=require('./watch-uncaught.js');
const t=JSDOM?test:test.skip;

const envs=[];
afterEach(()=>{ while(envs.length) envs.pop().w.close(); });

const when=(s)=>new Date(Date.UTC(2026,9,26,12,0,0)+s*1000).toISOString();
const node=(group,index,over={})=>({reviewed:{group,index,id:'vm-'+index,cid:'c'+index},matched:{group,index,id:'vm-'+index,cid:'c'+index},phase:'settled',status:'succeeded',exit_code:0,started_at:when(0),ended_at:when(5),stdout_kept:3,stdout_received:3,stderr_kept:0,stderr_received:0,output_path:'command-output/x',...over});
const task=(inst={})=>({id:'cmd-1',kind:'command',status:'completed',name:'Run command',total_count:1,completed_count:1,failed_count:0,created_at:when(0),
 command:{command:'uptime',run_as:'vcap',shell:'/bin/bash',timeout_seconds:600,parallel:false,max_in_flight:1},
 instances:[{instance_id:'val-a',instance_name:'val-a-name',deployment_name:'dep-val-a',status:'success',command:{vms:[node('node',0),node('node',1)]},...inst}]});

const enabled=[['/b/config/ssh/ui-terminal-status',(u,o,j)=>j(200,{enabled:true})]];
const start=async(record,opts={})=>{
 const env=boot({noDialogs:true,routes:enabled,...opts});envs.push(env);
 await sleep(60);
 if(opts.instances!==undefined)env.w.serviceInstances=opts.instances;
 env.w.BlacksmithRoutes=(await routesOf()).BlacksmithRoutes;
 env.w.__T.renderTaskDetail(record);
 return env;
};
const routesOf=async()=>{
 const dom=new JSDOM('<main id="console-main"><h1 id="console-title">Blacksmith</h1><div class="tab-content"></div></main>',{url:'http://localhost/#/',runScripts:'outside-only',pretendToBeVisual:true});
 const w=watchUncaught(dom.window);
 w.blacksmithViews={activate:async (r)=>r};
 w.eval(fs.readFileSync(path.resolve(__dirname,'..','..','ui','routes.js'),'utf8'));
 await sleep(40);
 return w;
};
const terminals=(d)=>[...d.querySelectorAll('#upgrade button.command-node-terminal')];

t('the instance heading links to the instance and a node icon links to its row in the VMs view',async()=>{
 const {w,d}=await start(task());
 const heading=d.querySelector('#upgrade .instance-heading a.instance-link');
 expect(heading.getAttribute('href')).toBe(w.BlacksmithRoutes.format({screen:'services',instance:'val-a',section:'details'}));
 expect(heading.textContent).toBe('val-a-name');
 const links=[...d.querySelectorAll('#upgrade a.command-node-link')];
 expect(links.length).toBe(2);
 const route=w.BlacksmithRoutes.parse(links[1].getAttribute('href'));
 expect(links[1].getAttribute('href')).toBe(w.BlacksmithRoutes.format({screen:'services',instance:'val-a',section:'vms',node:'node/1'}));
 expect(route).toMatchObject({screen:'services',instance:'val-a',section:'vms',node:'node/1'});
 expect(links[1].getAttribute('aria-label')).toBe('Show node/1 in the VMs view');
 expect(new Set(links.map((l)=>l.dataset.keepKey)).size).toBe(2);
});

t('the node parameter belongs to the VMs section only',async()=>{
 const w=await routesOf();
 const R=w.BlacksmithRoutes;
 expect(R.format({screen:'services',instance:'i1',section:'vms',node:'node/0'})).toContain('node=node%2F0');
 expect(R.format({screen:'services',instance:'i1',section:'summary',node:'node/0'})).not.toContain('node=');
 expect(R.parse('#/instances/i1/summary?node=node%2F0').node).toBeUndefined();
});

t('the terminal opens with the recorded deployment name even before the instance list loads',async()=>{
 const {w,d}=await start(task());
 const calls=[];
 w.TerminalManager.openWithContext=(...a)=>calls.push(a);
 expect(w.serviceInstances).toBeUndefined();
 const [button]=terminals(d);
 expect(button.getAttribute('aria-label')).toBe('Open a terminal on node/0');
 expect(button.querySelector('svg')).not.toBeNull();
 button.click();
 expect(calls).toEqual([['service-instance','val-a','node/0','val-a-name','dep-val-a']]);
});

t('the terminal button is hidden when SSH terminals are disabled',async()=>{
 const {d}=await start(task(),{routes:[['/b/config/ssh/ui-terminal-status',(u,o,j)=>j(200,{enabled:false,message:'off'})]]});
 await sleep(40);
 expect(terminals(d).length).toBe(2);
 expect(terminals(d).every((b)=>b.hidden)).toBe(true);
});

t('a deleted instance loses its links and says so, but an unloaded list does not count as deleted',async()=>{
 const gone=await start(task(),{instances:{other:{}}});
 expect(terminals(gone.d).length).toBe(0);
 expect(gone.d.querySelectorAll('#upgrade a.command-node-link').length).toBe(0);
 expect([...gone.d.querySelectorAll('#upgrade .command-node-gone')].map((e)=>e.textContent)).toEqual(['Instance no longer exists','Instance no longer exists']);
 const here=await start(task(),{instances:{'val-a':{}}});
 expect(terminals(here.d).length).toBe(2);
 const unloaded=await start(task());
 expect(terminals(unloaded.d).length).toBe(2);
 expect(unloaded.d.body.textContent).not.toContain('no longer exists');
});

t('SSH session tasks become links that open the task dialog, and an old record says they were not recorded',async()=>{
 const {w,d}=await start(task({ssh_sessions:[{group:'node',setup_task_id:41,cleanup_task_id:42},{group:'other',setup_task_id:43,cleanup_task_id:0}]}));
 const shown=[];
 w.showTaskDetails=async(id)=>shown.push(id);
 const links=[...d.querySelectorAll('#upgrade a.ssh-session-link')];
 expect(links.map((l)=>l.textContent)).toEqual(['SSH setup · BOSH task 41','SSH cleanup · BOSH task 42','SSH setup · BOSH task 43']);
 expect(new Set(links.map((l)=>l.dataset.keepKey)).size).toBe(3);
 links[1].click();
 expect(shown).toEqual([42]);
 const old=await start(task());
 expect(old.d.querySelectorAll('#upgrade a.ssh-session-link').length).toBe(0);
 expect(old.d.querySelector('#upgrade .ssh-sessions-missing').textContent).toBe("SSH session tasks weren't recorded for this run.");
});

t('a poll re-render keeps focus on the terminal button and the node link',async()=>{
 const {w,d}=await start(task());
 const [button]=terminals(d);
 button.focus();
 const key=button.dataset.keepKey;
 w.__T.renderTaskDetail(task());
 expect(d.activeElement.dataset.keepKey).toBe(key);
 expect(d.activeElement.classList.contains('command-node-terminal')).toBe(true);
 const link=d.querySelector('#upgrade a.command-node-link');
 link.focus();
 w.__T.renderTaskDetail(task());
 expect(d.activeElement.dataset.keepKey).toBe(link.dataset.keepKey);
});

const vmsPage=async(vms)=>{
 const env=boot({noDialogs:true});envs.push(env);
 await sleep(60);
 const {d}=env;
 const host=d.createElement('div');host.id='services';
 const content=d.createElement('div');content.className='detail-content';
 content.innerHTML=env.w.__T.formatVMs(vms,'val-a');
 host.append(content);d.body.append(host);
 return env;
};
const vm=(job,index)=>({job_name:job,index,id:'id-'+job+index,state:'running',job_state:'running',ips:['10.0.0.'+(index+1)],az:'z1',resurrection_paused:false});

t('the VMs view marks each row with its node and highlights the linked one',async()=>{
 const {w,d}=await vmsPage([vm('node',0),vm('node',1)]);
 const rows=[...d.querySelectorAll('tr.vm-row')];
 expect(rows.map((r)=>r.dataset.vmNode)).toEqual(['node/0','node/1']);
 expect(w.highlightVmNode('node/1')).toBe(true);
 expect(rows[1].classList.contains('vm-row-target')).toBe(true);
 expect(rows[0].classList.contains('vm-row-target')).toBe(false);
 expect(w.highlightVmNode('node/0')).toBe(true);
 expect(d.querySelectorAll('tr.vm-row-target').length).toBe(1);
});

t('a node that no longer exists is announced and nothing else is highlighted',async()=>{
 const {w,d}=await vmsPage([vm('node',0)]);
 expect(w.highlightVmNode('node/9')).toBe(false);
 expect(d.querySelectorAll('tr.vm-row-target').length).toBe(0);
 const note=d.querySelector('.vm-node-missing');
 expect(note.getAttribute('role')).toBe('status');
 expect(note.textContent).toBe('Node node/9 no longer exists.');
 w.highlightVmNode('');
 expect(d.querySelector('.vm-node-missing')).toBeNull();
});

t('the target row has a visible highlight style',()=>{
 const css=fs.readFileSync(path.resolve(__dirname,'..','..','ui','blacksmith.css'),'utf8');
 expect(css).toMatch(/\.vm-row\.vm-row-target/);
});

t('a failed VM fetch keeps its error and says nothing about the node',async()=>{
 const {w,d}=await vmsPage([vm('node',0)]);
 const content=d.querySelector('#services .detail-content');
 content.innerHTML='<div class="error">Failed to load vms: HTTP 503: Service Unavailable</div>';
 expect(w.highlightVmNode('node/0')).toBe(false);
 expect(d.querySelector('.vm-node-missing')).toBeNull();
 expect(content.textContent).toContain('Failed to load vms');
});

t('a node link that waited on a failed load is highlighted once a later load works',async()=>{
 const {w,d}=await vmsPage([vm('node',0)]);
 const content=d.querySelector('#services .detail-content');
 content.innerHTML='<div class="error">Failed to load vms: HTTP 503</div>';
 w.highlightVmNode('node/0');
 const w2=w;
 content.innerHTML=w2.__T.formatVMs([vm('node',0),vm('node',1)],'val-a');
 w2.__T.applyPendingVmNode();
 expect(d.querySelector('tr.vm-row-target')?.dataset.vmNode).toBe('node/0');
 expect(d.querySelector('.vm-node-missing')).toBeNull();
});

t('an empty loaded inventory still reports the node as gone',async()=>{
 const {w,d}=await vmsPage([]);
 expect(w.highlightVmNode('node/0')).toBe(false);
 expect(d.querySelector('.vm-node-missing').textContent).toBe('Node node/0 no longer exists.');
});
