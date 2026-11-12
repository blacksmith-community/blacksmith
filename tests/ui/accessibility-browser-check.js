// Run after browser-check.js; preload axe-core as described in the UI test README.
async (page) => {
 const rail=async(page,selector)=>{
  // Follows a rail link the way an operator would: through the phone drawer
  // under 700px, and by opening the link's area when it is collapsed.
  if(await page.locator('#rail-toggle').isVisible()) await page.locator('#rail-toggle').click();
  const link=page.locator('#console-rail').locator(selector).first();
  if(!await link.isVisible()) await page.locator('#console-rail .rail-area').filter({has:page.locator(selector)}).locator(':scope > .rail-area-toggle').click();
  await link.click();
 };

 await page.setViewportSize({width:1440,height:1000});
 await page.keyboard.press('Escape');
 if (!await page.evaluate(() => typeof axe !== 'undefined')) throw new Error('Preload axe-core with page.addScriptTag before running this check.');
 const results=[];
 for (const width of [1440,320]) for (const theme of ['light','dark','high-contrast-light','high-contrast-dark']) {
  await page.setViewportSize({width,height:900});
  await page.evaluate(t=>{window.blacksmithTheme.setPreference(t);},theme);
  await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));
  for (const screen of ['blacksmith','services']) {
   if(screen==='services') await page.evaluate(()=>window.blacksmithRouter.navigate({screen:'services',instance:'cache-1',section:'details'}));
   const group=screen==='services'?'.rail-instance':'#rail-sections-blacksmith';
   const ids=await page.locator(`#console-rail ${group} a.rail-link`).evaluateAll(es=>es.map(e=>e.dataset.section));
   for (const id of ids) {
    await rail(page,`${group} a[data-section="${id}"]`);
    await page.waitForFunction(s=>!document.querySelector(`#${s} .detail-content > .loading`),screen);
    await page.waitForTimeout(180);
    const violations=await page.evaluate(async()=> (await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21aa','wcag22aa']}})).violations.map(v=>({id:v.id,help:v.help,nodes:v.nodes.map(n=>({target:n.target,summary:n.failureSummary,html:n.html}))})));
    results.push({screen,id,theme,width,violations});
   }
  }
 }
 const failures=results.filter(r=>r.violations.length);
 if(failures.length) throw new Error(JSON.stringify(failures));
 return {auditedViews:results.length, violations:0};
}