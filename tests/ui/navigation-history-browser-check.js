// Run after accessibility-populated-fixture.js on the same local origin.
async page => {
 const checks=[]; const check=(v,m)=>{if(!v)throw new Error(m);checks.push(m);};
 const go=async r=>{const result=await page.evaluate(r=>blacksmithRouter.navigate(r),r);check(result,'Open '+JSON.stringify(r));await page.waitForFunction(()=>!blacksmithRouter.applying);};
 await page.reload();await page.waitForFunction(()=>window.blacksmithRouter?.current);
 await go({screen:'blacksmith',section:'plans'});
 await page.locator('.plan-item').first().click();await page.waitForFunction(()=>blacksmithRouter.current?.plan);
 const plan=await page.evaluate(()=>({...blacksmithRouter.current}));
 await go({screen:'blacksmith',section:'events'});await page.goBack();await page.waitForFunction(()=>blacksmithRouter.current?.plan&&!blacksmithRouter.applying);
 check(await page.locator('.plan-item.active').count()===1,'Back restores selected plan');
 for(const section of ['details','vms','tasks','events','blacksmith-logs','certificates','plans','broker','configs','config','manifest'])await go({screen:'blacksmith',section});
 for(const section of ['details','vms','events','instance-logs','certificates','credentials','testing','manifest'])await go({screen:'services',instance:'cache-1',section});
 await go({screen:'services',instance:'cache-1',section:'testing',subview:'set'});
 const field=page.locator('#services .operation-content input[type=text]').first();await field.fill('preserved-draft');
 await go({screen:'services',instance:'cache-2',section:'testing'});
 await go({screen:'services',instance:'cache-1',section:'testing',subview:'set'});
 check(await field.inputValue()==='preserved-draft','Testing draft survives another instance');
 check(await page.evaluate(()=>currentTestingInstance.id)==='cache-1','Testing uses restored instance context');
 await go({screen:'blacksmith',section:'certificates',subview:'trusted'});
 check(await page.locator('#cert-main-tablist .active').getAttribute('data-tab')==='trusted','Certificate subview route');
 await page.reload();await page.waitForFunction(()=>window.blacksmithRouter?.current?.subview==='trusted'&&!blacksmithRouter.applying);
 check(await page.locator('#cert-main-tablist .active').getAttribute('data-tab')==='trusted','Deep link reload restores subview');
 await go({screen:'blacksmith',section:'manifest',subview:'manifest'});
 await go({screen:'blacksmith',section:'plans'});
 await page.setViewportSize({width:390,height:900});await page.locator('.plan-item').first().click();await page.waitForFunction(()=>blacksmithRouter.current?.plan&&!blacksmithRouter.applying);
 check(await page.locator('.plan-collection-link').isVisible(),'Mobile plan return link visible');
 check(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'Mobile page has no horizontal overflow');
 await page.locator('.plan-collection-link').click();await page.waitForFunction(()=>!blacksmithRouter.current.plan&&!blacksmithRouter.applying);
 check(await page.locator('.plans-list').isVisible(),'Mobile returns to plan groups');
 await page.setViewportSize({width:1440,height:1000});
 return {checks};
}
