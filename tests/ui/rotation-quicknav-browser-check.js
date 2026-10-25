// Run after the local batch-flow fixture. Never starts an operation.
async page=>{
 for(const [query,mode,after] of [['rot pat un','patch','pending'],['rot pa con','patch','immediate'],['rot red','redeploy','']]){
  await page.locator('#navigation-trigger').click();
  await page.locator('#navigation-search').fill(query);
  if(await page.locator('#navigation-results [role="option"]').count()!==1)throw new Error('Ambiguous completion: '+query);
  await page.keyboard.press('Enter');
  await page.waitForFunction(()=>!document.getElementById('navigation-dialog').open);
  if(await page.locator(`#rotate-mode-${mode}-tab`).getAttribute('aria-selected')!=='true')throw new Error('Wrong mode: '+query);
  if(after&&!await page.locator(`#rotate-mode-converge-${after}`).isChecked())throw new Error('Wrong after-patching choice');
  if(!await page.locator('[data-batch-step="1"]').evaluate(e=>e.getAttribute('aria-current')==='step'))throw new Error('Not on Options');
 }
 return {shortcuts:3,passed:true};
}
