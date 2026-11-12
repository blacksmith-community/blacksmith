const {test,expect,afterEach}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const UI=path.resolve(__dirname,'..','..','ui');
const strip=(css)=>css.replace(/\/\*[\s\S]*?\*\//g,'');
const bodies=(file,selector)=>{
 const out=[];
 strip(fs.readFileSync(path.join(UI,file),'utf8')).replace(/([^{}]+)\{([^{}]*)\}/g,(m,sel,body)=>{
  if(sel.split(',').map((s)=>s.replace(/\s+/g,' ').trim()).includes(selector)) out.push(body);
  return m;
 });
 return out.join(';');
};
const envs=[];
afterEach(()=>{ while(envs.length) envs.pop().w.close(); });

const when=(s)=>new Date(Date.UTC(2026,9,26,12,0,0)+s*1000).toISOString();
const certTask=()=>({id:'cert-1',kind:'certificates',status:'completed',certificates:{mode:'redeploy',reason:'rotate'},total_count:1,completed_count:1,failed_count:0,created_at:when(0),
 instances:[{instance_id:'i1',instance_name:'certmgmt-valkey-c3',deployment_name:'valkey-cluster-3-9b4685ff',status:'success',certificates:{phase:'done',variables:[]}}]});

const CSS='blacksmith.css';

t('the summary table is the rounded box and fills it',()=>{
 const box=bodies(CSS,'#upgrade .task-detail-summary');
 expect(box).toMatch(/border-radius:\s*\.5rem/);
 expect(box).toMatch(/overflow:\s*hidden/);
 expect(box).toMatch(/padding:\s*0\b/);
 const table=bodies(CSS,'#upgrade .task-summary-table');
 expect(table).toMatch(/margin:\s*0\b/);
 expect(table).toMatch(/width:\s*100%/);
});

t('summary cells have visible padding and the headers are bold on a tint',()=>{
 const cell=bodies(CSS,'#upgrade .task-summary-table td');
 const pad=bodies(CSS,'#upgrade .task-summary-table th')+';'+cell;
 const px=[...pad.matchAll(/padding:\s*(\d+)px\s+(\d+)px/g)].map((m)=>[+m[1],+m[2]]);
 expect(px.length).toBeGreaterThan(0);
 for(const [v,h] of px){ expect(v).toBeGreaterThanOrEqual(6); expect(h).toBeGreaterThanOrEqual(6); expect(v).toBeLessThanOrEqual(10); }
 const th=bodies(CSS,'#upgrade .task-summary-table th');
 expect(th).toMatch(/font-weight:\s*700/);
 expect(th).toMatch(/background:\s*color-mix\(/);
});

t('the badge keeps its distance from the cell border',()=>{
 const cell=bodies(CSS,'#upgrade .task-summary-table th')+bodies(CSS,'#upgrade .task-summary-table td');
 expect(cell).toMatch(/padding:\s*\d+px\s+10px/);
});

t('the title sits close to the tab bar and its rule',()=>{
 expect(bodies(CSS,'#upgrade .upgrade-tasks:has(> .upgrade-task-detail)')).toMatch(/padding-top:\s*\.25rem/);
 const head=bodies(CSS,'#upgrade .task-detail-header');
 expect(head).toMatch(/padding-bottom:\s*\.25rem/);
 expect(head).toMatch(/margin-bottom:\s*\.5rem/);
});

t('the route row orders breadcrumbs, Updated, then the icons at the far right',()=>{
 expect(bodies(CSS,'#route-context .route-updated')).toMatch(/order:\s*1/);
 const guide=bodies(CSS,'#route-context > .view-guidance');
 expect(guide).toMatch(/order:\s*2/);
 expect(guide).toMatch(/margin-left:\s*auto/);
});

t('the deployment name shares the instance heading line',async()=>{
 const env=boot({noDialogs:true});envs.push(env);
 await sleep(60);
 env.w.__T.renderTaskDetail(certTask());
 const heading=env.d.querySelector('#upgrade .upgrade-task-instance .instance-heading');
 const dep=heading.querySelector('.instance-deployment');
 expect(dep.textContent).toBe('valkey-cluster-3-9b4685ff');
 expect(heading.querySelector('.instance-id').textContent).toBe('certmgmt-valkey-c3');
 expect(env.d.querySelectorAll('#upgrade .upgrade-task-instance .instance-deployment').length).toBe(1);
 const css=bodies(CSS,'#upgrade .instance-heading');
 expect(css).toMatch(/flex-wrap:\s*wrap/);
 expect(bodies(CSS,'#upgrade .instance-heading .instance-deployment')).toMatch(/var\(--text-secondary\)/);
});
