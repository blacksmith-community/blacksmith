const {test,expect,afterEach}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const envs=[];
afterEach(()=>{ while(envs.length) envs.pop().w.close(); });

// Each of these cells stands for a value the broker did not report, so it
// shows the shared muted dash and not a bare one.
const task=()=>({
 id:'job-1',name:'Rotate',kind:'certificates',status:'completed',paused:false,
 certificates:{mode:'redeploy',preview_id:'pv'},
 total_count:1,completed_count:1,failed_count:0,created_at:'2026-10-24T09:00:00Z',
 instances:[{instance_id:'i1',instance_name:'blacksmith-0',deployment_name:'dep',status:'success',
  certificates:{phase:'done',
   variables:[{name:'/dep/ca_crt',old_serial:'AA',new_serial:'BB',validation:[{name:'sans',passed:true}]}],
   vms:[{vm:'vm/0',phase:'done',processes:[{state:'running'}],targets:[{variable:'/dep/ca_crt',method:'tls',ok:true}]}]}}]
});

t('an unreported detail, target note, or process name shows the muted dash',async()=>{
 const env=boot({noDialogs:true});
 envs.push(env);
 await sleep(60);
 env.w.__T.renderTaskDetail(task());
 const d=env.d;
 const detail=d.querySelector('.cert-validation-table tbody tr td:nth-child(3)');
 const named=(caption)=>[...d.querySelectorAll('.cert-task-vm-body table')].find((x)=>x.querySelector('caption').textContent===caption);
 const note=named('Verification').querySelector('tbody td[data-label="Details"]');
 const process=named('Processes').querySelector('tbody td');
 for(const cell of [detail,note,process]){
  expect(cell).not.toBeNull();
  const mark=cell.querySelector('span.empty-value');
  expect(mark).not.toBeNull();
  expect(mark.textContent).toBe('—');
  expect(cell.textContent).toBe('—');
 }
});
