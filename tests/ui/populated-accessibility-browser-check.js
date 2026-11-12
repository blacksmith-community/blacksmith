// Run after batch-browser-check.js and preloading axe-core.
async page => {
 const rail=async(page,selector)=>{
  // Follows a rail link the way an operator would: through the phone drawer
  // under 700px, and by opening the link's area when it is collapsed.
  if(await page.locator('#rail-toggle').isVisible()) await page.locator('#rail-toggle').click();
  const link=page.locator('#console-rail').locator(selector).first();
  if(!await link.isVisible()) await page.locator('#console-rail .rail-area').filter({has:page.locator(selector)}).locator(':scope > .rail-area-toggle').click();
  await link.click();
 };

 if (!await page.evaluate(() => typeof axe !== 'undefined')) throw new Error('Preload axe-core before this check.');
 const results=[];
 for(const theme of ['light','dark','high-contrast-light','high-contrast-dark']){
  await page.evaluate(t=>{window.blacksmithTheme.setPreference(t);},theme);
  await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));
  for(const width of [1440,390]){
   await page.setViewportSize({width,height:900});
   for(const screen of ['upgrade','certificates']){
    await rail(page,screen==='upgrade'?'#batch-new':'a[data-screen="certificates"]');
    await page.waitForTimeout(300);
    await page.locator(`#${screen}`).evaluate(e=>e.scrollTop=0);
    const violations=await page.evaluate(async()=>(await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21aa','wcag22aa']}})).violations.map(v=>({id:v.id,nodes:v.nodes.map(n=>({target:n.target,summary:n.failureSummary}))})));
    results.push({theme,width,screen,violations});
   }
  }
 }
 await rail(page,'#batch-new');
 await page.locator('#batch-flow-next').click();
 await page.locator('#batch-flow-review .batch-review-title').first().waitFor();
 const modalViolations=await page.evaluate(async()=>(await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21aa','wcag22aa']}})).violations.map(v=>({id:v.id,nodes:v.nodes.map(n=>({target:n.target,summary:n.failureSummary}))})));
 const screens=results.filter(r=>r.violations.length);
 if(screens.length || modalViolations.length) throw new Error(JSON.stringify({screens,modalViolations}));
 await page.locator('#batch-flow-back').click();
 return {auditedViews:results.length,modalViolations:0};
}