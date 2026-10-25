const {test,expect}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;
const REL=[
 {name:'zeta-agent',release_versions:[{version:'3.0'},{version:'2.0'}]},
 {name:'bpm',release_versions:[{version:'1.4.26'},{version:'1.4.0'}]},
 {name:'valkey-forge',release_versions:[{version:'9.1'},{version:'9.0'}]}
];
async function setup(){
 const env=boot(),{w,d}=env;
 w.__routes.push(['/b/bosh/stemcells',(u,o,j)=>j(200,[])],['/b/bosh/releases',(u,o,j)=>j(200,REL)],['/b/bosh/deployment-releases',(u,o,j)=>j(200,{'small-cache':['valkey-forge/9.0','bpm/1.4.0']})],['/b/upgrade/settings',(u,o,j)=>j(200,{})],['/b/upgrade/tasks',(u,o,j)=>j(200,[])],['/b/upgrade/preview',(u,o,j)=>j(200,{id:'p1',diverged:[]})]);
 await sleep(30);
 w.plansData={services:[{id:"valkey",name:"valkey"}]};w.serviceInstances={cache:{service_id:'valkey',plan_id:'small',deployment_name:'small-cache',plan:{id:'small',releases:['valkey-forge']}}};
 d.getElementById('upgrade').classList.add('active');
 await w.__T.initUpgradeTab();await sleep(60);
 return env;
}
const advrel=(d)=>[...d.querySelectorAll('[id^="upgrade-advrel-"]')];
const pick=(w,sel,v)=>{sel.value=v;sel.dispatchEvent(new w.Event('change',{bubbles:true}));};
const setService=(w,d,v)=>pick(w,d.getElementById("upgrade-service-filter"),v);

t('with All services the Advanced section lists the director releases, sorted',async()=>{
 const {w,d}=await setup();try{
 const sels=advrel(d);
 expect(sels.map(s=>s.dataset.releaseName)).toEqual(['bpm','valkey-forge','zeta-agent']);
 sels.forEach(s=>{expect(s.value).toBe('');expect(s.options[0].textContent).toBe("Don't change");});
 expect([...sels[2].options].map(o=>o.value)).toEqual(['','3.0','2.0']);
 expect(d.querySelector('.upgrade-advanced-releases').textContent).not.toContain('Select a service');
 expect(d.querySelector('.upgrade-advanced > .batch-field-help').textContent).toContain('only on instances whose deployment already uses it');
 }finally{w.close();}
});
t('with one service its own release is left out',async()=>{
 const {w,d}=await setup();try{
 setService(w,d,'valkey');await sleep(10);
 expect(advrel(d).map(s=>s.dataset.releaseName)).toEqual(['bpm','zeta-agent']);
 }finally{w.close();}
});
t('switching the Service filter keeps a version already chosen',async()=>{
 const {w,d}=await setup();try{
 pick(w,d.getElementById('upgrade-advrel-zeta-agent'),'2.0');
 setService(w,d,'valkey');await sleep(10);
 expect(d.getElementById('upgrade-advrel-zeta-agent').value).toBe('2.0');
 setService(w,d,'');await sleep(10);
 expect(d.getElementById('upgrade-advrel-zeta-agent').value).toBe('2.0');
 }finally{w.close();}
});
t('with All and one Advanced release chosen the preview names only that release',async()=>{
 const {w,d,calls}=await setup();try{
 pick(w,d.getElementById('upgrade-advrel-bpm'),'1.4.26');
 w.__T.selectedUpgradeInstances.add('cache');
 w.__T.startUpgrade({flowReview:true});await sleep(30);
 const call=calls.find(c=>c.startsWith('POST /b/upgrade/preview'));
 expect(Boolean(call)).toBe(true);
 expect(JSON.parse(call.slice(call.indexOf('{'))).target_releases).toEqual([{name:'bpm',version:'1.4.26'}]);
 }finally{w.close();}
});
t('with All and nothing chosen the start is blocked inline and sends nothing',async()=>{
 const {w,d,calls,alerts}=await setup();try{
 w.__T.setBatchStep(1,false);
 const next=d.getElementById('batch-flow-next');
 expect(next.disabled).toBe(true);
 expect(d.getElementById('batch-flow-help').textContent).toBe('Choose a stemcell or release target to continue.');
 w.__T.selectedUpgradeInstances.add('cache');
 await w.__T.startUpgrade({flowReview:false});await sleep(10);
 expect(calls.some(c=>c.includes('/b/upgrade/preview')||c.includes('POST /b/upgrade/tasks'))).toBe(false);
 expect(alerts).toEqual([]);
 }finally{w.close();}
});
