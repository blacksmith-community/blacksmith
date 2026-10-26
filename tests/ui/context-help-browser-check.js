// Run after the populated API fixture.
async page => {
 const routes=[{screen:'services'},{screen:'certificates'},{screen:'upgrade',view:'new',operation:'upgrade',step:0},{screen:'upgrade',view:'activity'}];
 for(const section of ['details','vms','tasks','events','blacksmith-logs','certificates','plans','broker','configs','config','manifest'])routes.push({screen:'blacksmith',section});
 for(const section of ['details','vms','events','instance-logs','certificates','credentials','testing','manifest'])routes.push({screen:'services',instance:'cache-1',section});
 for(const route of routes){
  await page.evaluate(r=>blacksmithRouter.navigate(r),route);
  if(await page.locator('#route-context .view-guidance-button').count()!==1||await page.locator('.tab-content .view-guidance').count())throw new Error('Help misplaced: '+JSON.stringify(route));
 }
 await page.evaluate(()=>blacksmithRouter.navigate({screen:'services'}));
 await page.addScriptTag({path:'/private/tmp/blacksmith-batch-test/node_modules/axe-core/axe.min.js'});
 for(const width of [1440,320])for(const theme of ['light','dark','high-contrast-light','high-contrast-dark']){
  await page.setViewportSize({width,height:800});await page.evaluate(t=>blacksmithTheme.setPreference(t),theme);
  await page.locator('.view-guidance-button').hover();await page.waitForTimeout(150);
  const result=await page.evaluate(async()=>{const tip=document.querySelector('.view-guidance-tip'),rect=tip.getBoundingClientRect();return {visible:!tip.hidden&&rect.width>0,fits:rect.left>=0&&rect.right<=innerWidth&&rect.top>=0&&rect.bottom<=innerHeight,extra:document.querySelectorAll('.action-tooltip').length,violations:(await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21aa','wcag22aa']}})).violations.map(v=>v.id)};});
  if(!result.visible||!result.fits||result.extra||result.violations.length)throw new Error(JSON.stringify({width,theme,...result}));
  await page.keyboard.press('Escape');if(await page.locator('.view-guidance-tip').isVisible())throw new Error('Escape failed');await page.mouse.move(1,1);
 }
 await page.locator('.view-guidance-button').click();await page.mouse.move(1,1);await page.waitForTimeout(200);
 if(!await page.locator('.view-guidance-tip').isVisible())throw new Error('Tap/click pin failed');
 await page.locator('#logo').click();if(await page.locator('.view-guidance-tip').isVisible())throw new Error('Outside dismissal failed');
 return {views:routes.length,themeViewportChecks:8,violations:0};
}
