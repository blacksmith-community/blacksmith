const {test,expect}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
test('broker endpoint toolbar is ordered and the add dialog opens accessibly',async()=>{
 const {w,d}=boot();await sleep(100);try{
 const host=d.createElement('div');host.innerHTML=w.__T.renderBrokerTab();d.body.appendChild(host);
 const toolbar=host.querySelector('.broker-endpoint-header');expect(!!toolbar.querySelector('table')).toBe(false);
 expect(toolbar.querySelector('label').textContent).toBe('Endpoint:');
 expect(toolbar.lastElementChild.id).toBe('connection-status');expect(!!host.querySelector('.broker-details-table')).toBe(false);
 const opener=toolbar.querySelector('[title="Add Endpoint"]');opener.focus();w.showCFEndpointForm();
 const modal=d.getElementById('cf-endpoint-modal');expect(modal.classList.contains('active')).toBe(true);expect(modal.getAttribute('aria-hidden')).toBe('false');
 expect(d.activeElement.id).toBe('cf-endpoint-name');
 w.hideCFEndpointForm();expect(modal.classList.contains('active')).toBe(false);expect(d.activeElement).toBe(opener);
 }finally{w.close();}
});

