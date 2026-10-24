// Run after batch-browser-check.js and preloading axe-core.
async page => {
 if (!await page.evaluate(() => typeof axe !== 'undefined')) throw new Error('Preload axe-core before this check.');
 const results=[];
 for(const theme of ['light','dark']){
  await page.evaluate(t=>{document.documentElement.dataset.theme=t;document.documentElement.style.colorScheme=t;},theme);
  for(const width of [1440,390]){
   await page.setViewportSize({width,height:900});
   for(const screen of ['upgrade','certificates']){
    await page.locator(`.tabs .tab-button[data-tab="${screen}"]`).click();
    await page.waitForTimeout(300);
    await page.locator(`#${screen}`).evaluate(e=>e.scrollTop=0);
    const violations=await page.evaluate(async()=>(await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21aa','wcag22aa']}})).violations.map(v=>({id:v.id,nodes:v.nodes.map(n=>({target:n.target,summary:n.failureSummary}))})));
    results.push({theme,width,screen,violations});
   }
  }
 }
 await page.locator('.tabs .tab-button[data-tab="upgrade"]').click();
 await page.locator('#batch-flow-next').click();
 await page.locator('#batch-flow-review > h3').first().waitFor();
 const modalViolations=await page.evaluate(async()=>(await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21aa','wcag22aa']}})).violations.map(v=>({id:v.id,nodes:v.nodes.map(n=>({target:n.target,summary:n.failureSummary}))})));
 const screens=results.filter(r=>r.violations.length);
 if(screens.length || modalViolations.length) throw new Error(JSON.stringify({screens,modalViolations}));
 await page.locator('#batch-flow-back').click();
 return {auditedViews:results.length,modalViolations:0};
}