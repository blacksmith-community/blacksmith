// Run after accessibility-populated-fixture.js and injecting local axe-core.
async page => {
 const results=[];
 const audit=async(screen,theme,width)=>{
  await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));
  const violations=await page.evaluate(async()=>(await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21aa','wcag22aa']}})).violations.map(v=>({id:v.id,nodes:v.nodes.map(n=>({target:n.target,summary:n.failureSummary}))})));
  results.push({screen,theme,width,violations});
 };
 for(const theme of ['light','dark','high-contrast-light','high-contrast-dark']) for(const width of [1440,390]) {
  await page.setViewportSize({width,height:900});
  await page.evaluate(t=>window.blacksmithTheme.setPreference(t),theme);
  await page.locator('#navigation-trigger').click();
  await page.locator('#navigation-search').fill('theme');
  await audit('quick navigation',theme,width);
  await page.keyboard.press('Escape');
  await page.waitForTimeout(350); // Let native top-layer closing transitions finish.
  await page.locator('#keyboard-help-trigger').click();
  await audit('keyboard help',theme,width);
  await page.keyboard.press('Escape');
  await page.waitForTimeout(350); // Let native top-layer closing transitions finish.
  await page.evaluate(()=>window.showTaskDetails(71));
  await page.locator('#task-details-modal.active').waitFor();
  await audit('task details',theme,width);
  await page.keyboard.press('Shift+Tab');
  if(!await page.locator('#task-details-modal').evaluate(el=>el.contains(document.activeElement))) throw new Error('Task focus escaped');
  await page.keyboard.press('Escape');
  await page.waitForTimeout(350); // Let native top-layer closing transitions finish.
  await page.evaluate(()=>window.showConfigDetails('42'));
  await audit('config details',theme,width);
  await page.keyboard.press('Escape');
  await page.waitForTimeout(350); // Let native top-layer closing transitions finish.
 }
 const failures=results.filter(r=>r.violations.length);
 if(failures.length)throw new Error(JSON.stringify(failures));
 return {audited:results.length,failures};
}
