async page => {
 // Load axe-core into the fixture page before running this audit.
 const results=[];
 const next=page.locator('#batch-flow-next');
 const audit=async(mode,step,theme,width)=>{
  await page.locator('#upgrade').evaluate(el=>el.scrollTop=0);
  const violations=await page.evaluate(async()=>(await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21aa','wcag22aa']}})).violations.map(v=>({id:v.id,nodes:v.nodes.map(n=>({target:n.target,summary:n.failureSummary}))})));
  results.push({mode,step,theme,width,violations});
 };
 for(const theme of ['light','dark']) for(const width of [1440,390]){
  await page.setViewportSize({width,height:900});
  await page.evaluate(t=>{document.documentElement.dataset.theme=t;document.documentElement.style.colorScheme=t;},theme);
  for(const mode of ['upgrade','rotate','converge']){
   await page.locator('[data-batch-step="0"]').click();
   await page.locator(`#rotate-op-${mode}`).check();
   await audit(mode,'operation',theme,width);
   await next.click();
   if(mode==='upgrade') await page.locator('#upgrade-stemcell-filter').selectOption('ubuntu-jammy/1.90');
   await audit(mode,'options',theme,width);
   await next.click();
   await page.locator('#upgrade-select-all').click();
   await audit(mode,'instances',theme,width);
   await next.click();await page.locator('#batch-flow-review h3').waitFor();
   await audit(mode,'review',theme,width);
   await page.locator('#batch-flow-back').click();
  }
 }
 const failures = results.filter(r=>r.violations.length);
 if (failures.length) throw new Error(JSON.stringify(failures));
 return {audited:results.length,failures};
}