const {test,expect}=require('bun:test');
const {main:{boot,idle,TEST_LIMIT}}=require('./certificates/harness.js');
const t=(n,f,l)=>test(n,f,l===undefined?TEST_LIMIT:l);
t('header copies the displayed deployment name exactly',async()=>{
 const {w,d}=boot();await idle(w);const writes=[];w.navigator.clipboard.writeText=async value=>writes.push(value);
 const button=d.getElementById('copy-header-deployment');expect(button).not.toBe(null);
 expect(button.getAttribute('aria-label')).toBe('Copy deployment name');
 d.getElementById('deployment-name').textContent='ocfp-lab-blacksmith';button.click();await idle(w);expect(writes).toEqual(['ocfp-lab-blacksmith']);
 d.getElementById('deployment-name').textContent='updated-deployment';button.click();await idle(w);expect(writes[1]).toBe('updated-deployment');
 d.getElementById('deployment-name').textContent='';button.click();await idle(w);expect(writes.length).toBe(2);w.close();
});
