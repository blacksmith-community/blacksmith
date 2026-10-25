// Run after batch-flow-browser-check.js installs the local mocked fixture.
async (page) => {
 const check=(value,label)=>{if(!value)throw new Error(label);};
 await page.setViewportSize({width:2048,height:1024});
 await page.locator('[data-batch-step="0"]').click();
 await page.locator('#rotate-op-upgrade').check();
 await page.locator('#batch-flow-next').click();
 await page.locator('#upgrade-stemcell-filter').selectOption('ubuntu-jammy/1.90');
 await page.locator('#batch-flow-next').click();
 await page.locator('#upgrade-deselect-all').click();
 await page.locator('.upgrade-instance-item .batch-select-state').first().click();
 await page.locator('#batch-flow-next').click();
 await page.locator('#batch-flow-review > h3').first().waitFor();
 check(await page.locator('.batch-review-instances').getAttribute('open')!==null,'small selections are open');
 check(!await page.locator('.batch-review-instances .batch-pagination').isVisible(),'no single-page pager');
 check(await page.locator('.batch-review-instances .batch-instance-name').count()===1,'one named instance');
 const metrics=await page.locator('#batch-flow-review').evaluate(el=>({width:el.getBoundingClientRect().width,parent:el.parentElement.getBoundingClientRect().width,p:getComputedStyle(el.querySelector('.batch-review-impact p')).maxWidth,help:getComputedStyle(document.getElementById('batch-job-name-help')).maxWidth}));
 check(await page.locator('#upgrade').evaluate(el=>getComputedStyle(el).paddingBottom)==='0px','no extra padding below action bar');
 check(await page.locator('[data-batch-step="3"]').isEnabled(),'current review breadcrumb stays legible');
 check(metrics.width>metrics.parent*.95,'review uses available width');
 check(metrics.p==='none'&&metrics.help==='none','text has no arbitrary nested width caps');
 check(await page.locator('.batch-flow-footer').evaluate(el=>el.getBoundingClientRect().height)<=64,'compact desktop footer');
 await page.screenshot({path:'output/playwright/batch-review-single-desktop.png'});
 await page.setViewportSize({width:390,height:844});
 for(const theme of ['light','dark']){
  await page.evaluate(t=>{document.documentElement.dataset.theme=t;document.documentElement.style.colorScheme=t;},theme);
  check(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'review has no horizontal overflow');
  check(await page.locator('.batch-flow-footer').evaluate(el=>el.getBoundingClientRect().height)<=100,'compact mobile footer');
  check(await page.locator('#batch-flow-next').evaluate(el=>el.getBoundingClientRect().height)>=44,'touch target retained');
  await page.screenshot({path:`output/playwright/batch-review-single-${theme}.png`});
 }
 return {review:'passed',instances:1,themes:2};
}
