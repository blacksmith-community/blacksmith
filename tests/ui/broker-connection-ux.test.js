const {test,expect}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
test('broker endpoint toolbar is ordered and the add dialog opens accessibly',async()=>{
 const {w,d}=boot();await sleep(100);try{
 const host=d.createElement('div');host.innerHTML=w.__T.renderBrokerTab();d.body.appendChild(host);
 const toolbar=host.querySelector('.broker-endpoint-header');expect(!!toolbar.querySelector('table')).toBe(false);
 expect(toolbar.querySelector('label').textContent).toBe('Endpoint:');
 expect(toolbar.lastElementChild.id).toBe('connection-status');expect(!!host.querySelector('.broker-details-table')).toBe(false);
 const opener=toolbar.querySelector('[title="Add endpoint"]');opener.focus();w.showCFEndpointForm();
 const modal=d.getElementById('cf-endpoint-modal');expect(modal.open).toBe(true);expect(modal.hasAttribute('aria-hidden')).toBe(false);
 expect(d.activeElement.id).toBe('cf-endpoint-name');
 w.hideCFEndpointForm();expect(modal.open).toBe(false);expect(d.activeElement).toBe(opener);
 }finally{w.close();}
});

