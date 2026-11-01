// Local fixture only; run with playwright-cli run-code --filename=tests/ui/activity-consistency-browser-check.js.
async (page) => {
  const errors=[];page.on('pageerror',e=>errors.push(e.message));
  const check=(v,label)=>{if(!v)throw new Error(label);};
  const task={id:'patch-job',name:'Rotate RabbitMQ certificates',kind:'certificates',status:'running',total_count:1,completed_count:0,failed_count:0,created_at:'2026-10-25T14:00:00Z',certificates:{mode:'redeploy'},instances:[{instance_id:'rabbit',instance_name:'RabbitMQ cluster',deployment_name:'rabbitmq-cluster',status:'running',certificates:{phase:'converge',variables:[{name:'/ocfp-lab/rabbitmq-cluster/rabbitmq_cluster_crt',old_serial:'0D28778ABEC7D114B9F7710B99BF17C5F6A45B2E',new_serial:'68ECDCFBCE87D60C0FF1C37DB3CDFE2C6CA5B837',old_not_after:'2029-10-24',new_not_after:'2029-10-25',regenerate_state:'confirmed',validation:[{name:'key_match',passed:true,detail:'The private key matches the new certificate.'},{name:'not_after',passed:true,detail:'The new certificate expires later than the old one.'}]}],converge:{bosh_task_id:71},vms:Array.from({length:3},(_,i)=>({vm:`node/${i}`,phase:'done',files:[{path:'/var/vcap/data/jobs/rabbitmq/long-package-hash/tls/rabbitmq.crt',role:'certificate',state:'replaced'}],processes:['rabbitmq'],targets:[{variable:'/lab/rabbitmq/rabbitmq_cluster_crt',method:'none',ok:true,detail:'applied, not externally verified'}],gate_after:{ok:true}}))}}]};
  await page.route('**/v2/catalog',r=>r.fulfill({json:{services:[]}}));
  await page.route('**/b/**',r=>{
    if(r.request().method()!=='GET')return r.fulfill({status:403,json:{error:'Writes blocked'}});
    const path=new URL(r.request().url()).pathname;
    let json={};
    if(path==='/b/status')json={env:'Local fixture',instances:{},plans:{}};
    else if(path==='/b/upgrade/tasks')json=[task,{...task,id:'upgrade-job',kind:'upgrade',name:'Upgrade system',status:'paused',total_count:4,completed_count:1,failed_count:0,skipped_count:1,cancelled_count:1,unknown_count:1}];
    else if(path==='/b/upgrade/tasks/patch-job')json=task;
    else if(path==='/b/upgrade/tasks/upgrade-job')json={...task,id:'upgrade-job',kind:'upgrade',name:'Upgrade system',status:'paused',target_stemcell:'1.90'};
    else if(path==='/b/tasks/71')json={task:{id:71,state:'done',description:'Converge deployment',deployment:'rabbitmq-cluster',timestamp:1792936800},events:[]};
    else if(/\/(tasks|events|vms|releases|stemcells|registrations)$/.test(path))json=[];
    return r.fulfill({json});
  });
  await page.setViewportSize({width:1440,height:1000});
  await page.goto('http://127.0.0.1:4209');
  await page.locator('.tabs [data-tab="upgrade"]').click();
  await page.locator('#batch-activity').click();
  await page.locator('[data-task-id="upgrade-job"]').focus();
  await page.keyboard.press('Enter');
  await page.waitForFunction(()=>document.activeElement?.matches('.task-detail-header h3'));
  check((await page.locator('.task-detail-summary').innerText()).includes('System:'),'upgrade wording matches options');
  await page.getByRole('button',{name:'Back to Jobs'}).click();
  check(await page.locator('[data-task-id="upgrade-job"]').evaluate(el=>el===document.activeElement),'Back restores selected card focus');
  check((await page.locator('[data-task-id="upgrade-job"]').innerText()).includes('outcome unknown'),'mixed outcomes remain visible');
  await page.locator('[data-task-id="patch-job"]').click();
  await page.locator('.bosh-task-link').click();
  await page.locator('#task-details-modal').waitFor({state:'visible'});
  await page.locator('#task-details-modal .modal-close').click();
  await page.locator('#task-details-modal').waitFor({state:'hidden'});
  await page.evaluate(()=>{const host=document.createElement('div');host.id='fixture-result';document.querySelector('#upgrade .upgrade-right-panel').prepend(host);window.blacksmithResultPanel(host,{title:'Rotation started',taskId:'patch-job'});});
  await page.getByRole('button',{name:'View batch job',exact:true}).click();
  await page.waitForFunction(()=>document.activeElement?.matches('.task-detail-header h3'));
  await page.locator('.result-panel-close').click();
  check(await page.locator('.result-panel').count()===0,'notice dismissed');
  check(await page.locator('#batch-activity').evaluate(el=>el===document.activeElement),'close restores focus');
  check(await page.locator('.cert-task-vm').count()===3,'VM progress rendered');
  await page.locator('.cert-validation-details summary').click();
  check(await page.locator('.cert-task-comparison th').count()===3,'comparison has three columns');
  await page.locator('.cert-task-vm summary').first().click();
  for(const width of [1440,390]){
    await page.setViewportSize({width,height:1000});
    await page.locator('.cert-task-vm-body').first().scrollIntoViewIfNeeded();
    await page.screenshot({path:`output/playwright/activity-consistency-${width}.png`,fullPage:true});
    check(await page.locator('.cert-task-vm-body').first().isVisible(),'expanded evidence visible');
    check(await page.locator('.cert-task-vm-body').first().evaluate(el=>el.scrollWidth<=el.clientWidth+1),'tables fit available width');
  }
  check(errors.length===0,errors.join('\n'));
  return {controls:'passed',widths:[1440,390],errors};
}
