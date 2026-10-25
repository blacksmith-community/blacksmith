// Run after browser-check.js; preload axe-core as described in the UI test README.
async (page) => {
 await page.setViewportSize({width:1440,height:1000});
 await page.keyboard.press('Escape');
 if (!await page.evaluate(() => typeof axe !== 'undefined')) throw new Error('Preload axe-core with page.addScriptTag before running this check.');
 const results=[];
 for (const width of [1440,320]) for (const theme of ['light','dark','high-contrast-light','high-contrast-dark']) {
  await page.setViewportSize({width,height:900});
  await page.evaluate(t=>{window.blacksmithTheme.setPreference(t);},theme);
  await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));
  for (const screen of ['blacksmith','services']) {
   await page.locator(`.tab-button[data-tab="${screen}"]`).click();
   if(screen==='services') await page.evaluate(()=>window.navigateToInstance('cache-1'));
   const ids=await page.locator(`#${screen} .detail-tab`).evaluateAll(es=>es.map(e=>e.dataset.tab));
   for (const id of ids) {
    if(width < 700) await page.locator(`#${screen} .entity-section-select`).selectOption(id);
    else await page.locator(`#${screen} .detail-tab[data-tab="${id}"]`).click();
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