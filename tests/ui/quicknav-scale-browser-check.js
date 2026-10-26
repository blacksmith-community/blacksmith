// Run after the populated local API fixture. Uses synthetic instances only.
async page => {
 await page.reload();await page.waitForFunction(()=>window.blacksmithRouter&&!blacksmithRouter.applying);
 await page.evaluate(()=>{window.serviceInstances=Object.fromEntries(Array.from({length:10000},(_,i)=>['scale-'+i,{instance_name:'Cache '+i,service_id:'valkey',plan_id:'cluster'}]));});
 await page.locator('#navigation-trigger').click();const input=page.locator('#navigation-search');
 await input.fill('cache');await page.waitForFunction(()=>document.querySelector('#navigation-results').children.length===40);
 if(!await page.locator('.navigation-count').textContent().then(s=>s.includes('10,000')))throw new Error('Missing total');
 await page.locator('[data-load-more]').click();
 if(await page.locator('#navigation-results [role=option]').count()!==80)throw new Error('Result page did not append');
 await input.fill('9998 logs');await input.fill('9999 credentials');
 await page.waitForFunction(()=>document.querySelector('#navigation-results').children.length===1);
 if(!await page.locator('#navigation-results').textContent().then(s=>s.includes('Cache 9999')&&s.includes('Credentials')))throw new Error('Stale query result');
 await input.fill('cache');await page.waitForFunction(()=>document.querySelector('#navigation-results').children.length===40);
 await page.addScriptTag({path:'/private/tmp/blacksmith-batch-test/node_modules/axe-core/axe.min.js'});
 const checks=[];
 for(const theme of ['light','dark','high-contrast-light','high-contrast-dark'])for(const size of [{width:1440,height:900},{width:390,height:700},{width:320,height:568},{width:800,height:360}]){
  await page.setViewportSize(size);await page.evaluate(t=>blacksmithTheme.setPreference(t),theme);await page.waitForTimeout(100);
  const result=await page.evaluate(async()=>{const dialog=document.querySelector('#navigation-dialog');return {overflow:dialog.scrollWidth>dialog.clientWidth,violations:(await axe.run(dialog,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21aa','wcag22aa']}})).violations.map(v=>v.id)};});
  if(result.overflow||result.violations.length)throw new Error(JSON.stringify({theme,size,...result}));checks.push({theme,size});
 }
 return {instances:10000,initialRows:40,checks:checks.length,violations:0};
}
