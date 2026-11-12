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
