const {test,expect,afterEach}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const {watchUncaught}=require('./watch-uncaught.js');
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

const TITLE='Rotate 1 certificate on 1 instance';
const checks=[
 {name:'key_match',passed:true,detail:'public keys agree'},
 {name:'ca_verify',passed:true,detail:'chain verifies'},
 {name:'common_name',passed:true,detail:'cn ok'},
 {name:'sans',passed:true,detail:'sans ok'},
 {name:'ext_key_usage',passed:true,detail:'eku ok'},
 {name:'not_ca',passed:true,detail:'leaf'},
 {name:'not_before',passed:false,detail:'starts later'},
 {name:'not_after',passed:true,detail:'expires 2029'},
 {name:'weird_thing',passed:true,detail:'x'},
 {name:'chain_depth',skipped:true,passed:true,detail:'no CA supplied'}
];
const task=()=>({
 id:'job-1',name:TITLE,kind:'certificates',status:'completed',paused:false,
 certificates:{mode:'redeploy',preview_id:'pv'},
 total_count:1,completed_count:1,failed_count:0,created_at:'2026-10-24T09:00:00Z',
 instances:[{instance_id:'i1',instance_name:'blacksmith-0',deployment_name:'dep-one',status:'success',
  certificates:{phase:'done',variables:[{name:'/dep/ca_crt',old_serial:'AA',new_serial:'BB',validation:checks}]}}]
});
const start=async()=>{
 const env=boot({noDialogs:true});
 envs.push(env);
 await sleep(60);
 env.w.__T.renderTaskDetail(task());
 return env;
};

t('the heading is the job title and the summary labels have no colons',async()=>{
 const {d}=await start();
 expect(d.querySelector('#upgrade .task-detail-header h3').textContent).toBe(TITLE);
 const labels=[...d.querySelectorAll('#upgrade .task-detail-summary .label')].map((x)=>x.textContent);
 expect(labels).toContain('Mode');
 expect(labels).toContain('Status');
 expect(labels.some((x)=>x.endsWith(':'))).toBe(false);
});

t('the status is the same pill the Activity card uses',async()=>{
 const {d}=await start();
 const pill=d.querySelector('#upgrade .task-detail-summary .value .activity-status-label');
 expect(pill).not.toBeNull();
 expect(pill.textContent).toBe('Completed');
});

t('the job page marks the panel so the settings and the key prompt back link hide',async()=>{
 const {w,d}=await start();
 const panel=d.querySelector('#upgrade .upgrade-right-panel');
 expect(panel.classList.contains('job-detail')).toBe(true);
 for(const sel of ['#upgrade .upgrade-right-panel.job-detail > h2','#upgrade .upgrade-right-panel.job-detail .batch-execution-settings','#upgrade .upgrade-right-panel.job-detail .back-to-list']){
  expect(bodies('forge-theme.css',sel)).toMatch(/display:\s*none/);
 }
 w.backToTaskList();
 expect(panel.classList.contains('job-detail')).toBe(false);
});

t('the instance shows its name in the heading face with the phase as a pill',async()=>{
 const {d}=await start();
 const block=d.querySelector('#upgrade .upgrade-task-instance');
 const name=block.querySelector('.instance-id');
 expect(name.textContent).toBe('blacksmith-0');
 const pill=block.querySelector('.instance-heading .cert-task-phase.activity-status-label');
 expect(pill).not.toBeNull();
 expect(pill.textContent).toBe('Done');
 expect(block.querySelector('.instance-deployment').textContent).toBe('dep-one');
 expect(block.textContent).not.toMatch(/Phase:/);
 expect(bodies('blacksmith.css','#upgrade .upgrade-task-instance .instance-id')).not.toMatch(/monospace/);
});

t('the credential path is plain monospace with a copy button',async()=>{
 const {d}=await start();
 const name=d.querySelector('#upgrade .cert-task-variable-name');
 expect(name.textContent).toBe('/dep/ca_crt');
 const row=name.closest('.cert-task-variable-head');
 expect(row.querySelector('button.copy-btn')).not.toBeNull();
 const css=bodies('forge-theme.css','#upgrade .cert-task-variable-name');
 expect(css).toMatch(/font-weight:\s*400|font-weight:\s*normal/);
 expect(css).toMatch(/monospace/);
});

t('the validation table names each check and badges each result',async()=>{
 const {d}=await start();
 const rows=[...d.querySelectorAll('#upgrade .cert-validation-details tbody tr')];
 const cell=(r,i)=>r.querySelectorAll('td')[i];
 expect(rows.map((r)=>cell(r,0).textContent)).toEqual(['Key matches','CA verifies','Common name','SANs','Extended key usage','Not a CA','Not before','Not after','Weird_thing','Chain_depth']);
 const badge=(r)=>cell(r,1).querySelector('.cert-check-result');
 expect(badge(rows[0]).textContent).toBe('Passed');
 expect(badge(rows[0]).classList.contains('passed')).toBe(true);
 expect(badge(rows[6]).textContent).toBe('Failed');
 expect(badge(rows[6]).classList.contains('failed')).toBe(true);
 expect(badge(rows[9]).textContent).toBe('Skipped');
 expect(badge(rows[9]).classList.contains('skipped')).toBe(true);
 const table=d.querySelector('#upgrade .cert-validation-details table');
 expect(table.classList.contains('cert-validation-table')).toBe(true);
});

t('the validation columns size to content and Details takes the rest',()=>{
 const table=bodies('forge-theme.css','#upgrade .cert-validation-table');
 expect(table).toMatch(/table-layout:\s*auto/);
 const first=bodies('forge-theme.css','#upgrade .cert-validation-table th:nth-child(-n+2)');
 expect(first).toMatch(/width:\s*1%/);
 expect(first).toMatch(/white-space:\s*nowrap/);
});

t('the certificate changes header does not stick over the toolbar',()=>{
 const head=bodies('forge-theme.css','#upgrade .cert-evidence-table thead');
 expect(head).toMatch(/position:\s*static/);
});

const crumbs=async(hash,title)=>{
 const dom=new JSDOM('<main id="console-main"><h1 id="console-title">Blacksmith</h1><div class="tab-content"></div></main>',{url:'http://localhost/'+hash,runScripts:'outside-only',pretendToBeVisual:true});
 const w=watchUncaught(dom.window);
 w.blacksmithViews={activate:async (r)=>r};
 if(title) w.blacksmithJobTitle=(id)=>id==='job-1'?title:'';
 w.eval(fs.readFileSync(path.join(UI,'routes.js'),'utf8'));
 await sleep(40);
 return w;
};
const list=(w)=>[...w.document.querySelectorAll('nav[aria-label="Breadcrumb"] li')].map((li)=>li.textContent);

t('the breadcrumb ends with the job title',async()=>{
 const w=await crumbs('#/batch/activity/job-1',TITLE);
 expect(list(w)).toEqual(['Batch Operations','Activity',TITLE]);
 w.blacksmithRouter.destroy();w.close();
});

t('the breadcrumb falls back to the job id until the title is known',async()=>{
 const w=await crumbs('#/batch/activity/job-1');
 expect(list(w)).toEqual(['Batch Operations','Activity','Job job-1']);
 w.blacksmithRouter.destroy();w.close();
});

t('the breadcrumb refreshes once the page learns the title',async()=>{
 const w=await crumbs('#/batch/activity/job-1');
 w.blacksmithJobTitle=()=>TITLE;
 w.blacksmithRefreshContext();
 expect(list(w)).toEqual(['Batch Operations','Activity',TITLE]);
 w.blacksmithRouter.destroy();w.close();
});

t('a running job re-renders without rebuilding the route bar unless the title changes',async()=>{
 const {w}=await start();
 let rebuilds=0;
 w.blacksmithRefreshContext=()=>{rebuilds++;};
 const running=()=>({...task(),status:'running',completed_count:0});
 w.__T.renderTaskDetail(running());
 const first=rebuilds;
 w.__T.renderTaskDetail({...running(),completed_count:1});
 w.__T.renderTaskDetail({...running(),completed_count:2});
 expect(rebuilds).toBe(first);
 w.__T.renderTaskDetail({...running(),name:'A new title'});
 expect(rebuilds).toBe(first+1);
});

t('the job page publishes its title for the breadcrumb',async()=>{
 const {w}=await start();
 expect(w.blacksmithJobTitle('job-1')).toBe(TITLE);
});

t('returning to the same job rebuilds the route bar',async()=>{
 const {w}=await start();
 let rebuilds=0;
 w.blacksmithRefreshContext=()=>{rebuilds++;};
 const running=()=>({...task(),status:'running',completed_count:0});
 w.__T.renderTaskDetail(running());
 const first=rebuilds;
 w.backToTaskList();
 w.__T.renderTaskDetail(running());
 expect(rebuilds).toBe(first+1);
 w.dispatchEvent(new w.Event('hashchange'));
 w.__T.renderTaskDetail(running());
 expect(rebuilds).toBe(first+2);
});
