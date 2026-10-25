// Run after upgrade-options-browser-check.js, then inject axe-core.
async (page) => {
 const results=[];
 await page.locator('[data-dialog-role="cancel"]').click();
 for(const theme of ['light','dark','high-contrast-light','high-contrast-dark']) for(const width of [1440,390]){
  await page.setViewportSize({width,height:900});
  await page.evaluate(t=>{window.blacksmithTheme.setPreference(t);},theme);
  await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));
  for(const screen of ['options','picker']){
   if(screen==='picker') await page.locator('#batch-edit-releases').click();
   const violations=await page.evaluate(async()=>(await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21aa','wcag22aa']}})).violations.map(v=>({id:v.id,nodes:v.nodes.map(n=>({target:n.target,summary:n.failureSummary}))})));
   results.push({theme,width,screen,violations});
  }
  await page.locator('[data-dialog-role="cancel"]').click();
 }
 const failures=results.filter(result=>result.violations.length);
 if(failures.length) throw new Error(JSON.stringify(failures));
 return {audited:results.length,failures};
}
