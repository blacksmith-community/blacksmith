const {test,expect}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
test('log controls stay outside the scrolling table and its sortable headers',async()=>{
 const {w,d}=boot();await sleep(100);
 const host=d.createElement('div');host.innerHTML=w.__T.formatJobLogContent('node/0',{'app.log':'2026-10-25T10:00:00Z INFO ready'});
 expect(host.querySelectorAll('thead th').length).toBe(3);
 expect(host.querySelector('.table-controls-container').closest('table')).toBe(null);
 expect(host.querySelector('.logs-table-container').contains(host.querySelector('.table-controls-container'))).toBe(false);
 expect(host.querySelectorAll('[data-instance-log]').length).toBe(4);
 w.close();
});
