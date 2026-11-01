const {test,expect}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

t('the Add endpoint button shows the plus icon at the size of the Refresh button',async()=>{
 const {w,d}=boot();await sleep(100);try{
 const host=d.createElement('div');host.innerHTML=w.__T.renderBrokerTab();d.body.appendChild(host);
 const add=host.querySelector('.endpoint-controls button[aria-label="Add endpoint"]');
 expect(add).not.toBeNull();
 expect(add.title).toBe('Add endpoint');
 expect(add.textContent.trim()).toBe('');
 const icon=add.querySelector('svg.lucide-plus');
 expect(icon).not.toBeNull();
 const refresh=host.querySelector('button[aria-label="Refresh endpoints"] svg');
 expect(icon.getAttribute('width')).toBe(refresh.getAttribute('width'));
 expect(icon.getAttribute('height')).toBe('16');
 expect(add.getAttribute('onclick')).toBe('showCFEndpointForm()');
 }finally{w.close();}
});
