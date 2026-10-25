const {test,expect}=require('bun:test');
const {fleet:{boot,sleep}}=require('./harness.js');
test('inline certificate details reuse drawer sections and copy controls',async()=>{
 const {w,d,server}=await boot();try{
 const cert=server.certs.find(c=>c.id==='c2');cert.findings=[{severity:'warning',message:'Needs convergence'}];
 cert.observations=[{source:'credhub',locator:cert.key,version_id:'version-full',state:'ok'},{source:'probe',locator:'node/0 10.0.0.1:6379',state:'ok',matches:'latest'}];
 await w.blacksmithNav.switchTab('certificates');await sleep(120);w.blacksmithCerts.state.attention=false;await w.blacksmithCerts.refresh({announce:true});await sleep(80);
 d.querySelector('[data-cert-id="c2"] .cert-expand-btn').click();await sleep(20);
 const box=d.querySelector('.cert-expansion');expect(!!box).toBe(true);
 expect(!!box.querySelector('.cert-drawer-callouts')).toBe(true);expect(!!box.querySelector('.cert-credhub-table')).toBe(true);expect(!!box.querySelector('.cert-probe-table')).toBe(true);
 expect(box.querySelectorAll('.cert-obs-list,.cert-findings').length).toBe(0);
 const copied=[];w.navigator.clipboard.writeText=async value=>copied.push(value);
 box.querySelector('[aria-label="Copy endpoint node/0 10.0.0.1:6379"]').click();await sleep(5);expect(copied).toEqual(['node/0 10.0.0.1:6379']);
 expect(d.getElementById('cert-fleet-drawer').hidden).toBe(true);
 }finally{w.close();}
});
