const {test,expect,afterEach}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const envs=[];
afterEach(()=>{while(envs.length){const env=envs.pop();expect(env.alerts).toEqual([]);env.w.close();}});
async function setup(save){
 const env=boot({noDialogs:true}),{w}=env;envs.push(env);
 w.__routes.push(['/b/bosh/stemcells',(u,o,j)=>j(200,[])],['/b/bosh/releases',(u,o,j)=>j(200,[])],['/b/bosh/deployment-releases',(u,o,j)=>j(200,{})],['/b/upgrade/tasks',(u,o,j)=>j(200,[])],
  ['/b/upgrade/settings',(u,o,j)=>o&&o.method==='PUT'?save(j):j(200,{})]);
 await sleep(30);
 await w.__T.initUpgradeTab();await sleep(10);
 const shown=[];const original=w.Feedback.error;w.Feedback.error=(message,...rest)=>{shown.push(message);return original(message,...rest);};
 return {...env,shown};
}

t('a refused save reports through feedback and frees the button',async()=>{
 const {d,shown}=await setup(j=>j(500,{error:'the settings store is read-only'}));
 const button=d.getElementById('upgrade-save-settings');
 d.getElementById('upgrade-max-batch-jobs').value='2';
 button.click();await sleep(20);
 expect(shown).toEqual(['The setting wasn\'t saved. the settings store is read-only']);
 expect(button.disabled).toBe(false);
 expect(button.textContent).toBe('Save');
});

t('a failed request reports through feedback instead of a dialog',async()=>{
 const {d,shown}=await setup(()=>{throw new Error('network down');});
 const button=d.getElementById('upgrade-save-settings');
 button.click();await sleep(20);
 expect(shown).toEqual(['The setting wasn\'t saved. network down']);
 expect(button.disabled).toBe(false);
});
