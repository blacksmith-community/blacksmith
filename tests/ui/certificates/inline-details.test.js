const {test,expect}=require('bun:test');
const {fleet:{boot,idle},main:{TEST_LIMIT}}=require('./harness.js');
const t=(n,f,l)=>test(n,f,l===undefined?TEST_LIMIT:l);
t('inline certificate details reuse drawer sections and copy controls',async()=>{
 const {w,d,server}=await boot();try{
 const cert=server.certs.find(c=>c.id==='c2');cert.findings=[{severity:'warning',message:'Needs convergence'}];
 cert.observations=[{source:'credhub',locator:cert.key,version_id:'version-full',state:'ok'},{source:'probe',locator:'node/0 10.0.0.1:6379',state:'ok',matches:'latest'}];
 await w.blacksmithNav.switchTab('certificates');await idle(w);w.blacksmithCerts.state.attention=false;await w.blacksmithCerts.refresh({announce:true});await idle(w);
 d.querySelector('[data-cert-id="c2"] .cert-expand-btn').click();await idle(w);
 const box=d.querySelector('.cert-expansion');expect(!!box).toBe(true);
 expect(!!box.querySelector('.cert-drawer-callouts')).toBe(true);expect(!!box.querySelector('.cert-credhub-table')).toBe(true);expect(!!box.querySelector('.cert-probe-table')).toBe(true);
 expect(box.querySelectorAll('.cert-obs-list,.cert-findings').length).toBe(0);
 const copied=[];w.navigator.clipboard.writeText=async value=>copied.push(value);
 box.querySelector('[aria-label="Copy endpoint"]').click();await idle(w);expect(copied).toEqual(['node/0 10.0.0.1:6379']);
 expect(d.getElementById('cert-fleet-drawer').hidden).toBe(true);
 }finally{w.close();}
});
