const {test,expect}=require('bun:test');
const {main:{boot}}=require('./certificates/harness.js');
test('log counts share the pagination row rather than file controls',()=>{
 const {w,d}=boot();try{for(const more of [true,false]){
 const host=d.createElement('div');host.innerHTML=w.__T.formatBlacksmithLogs('INFO first\nINFO second',null,{total_lines:20,has_more:more});
 expect(!!host.querySelector('.logs-controls-row .logs-line-count')).toBe(false);
 expect(host.querySelector('.logs-pagination-row .logs-line-count')?.textContent).toBe('Showing 2 of 20 lines');
 expect(!!host.querySelector('.logs-pagination-row #logs-load-more')).toBe(more);
 }}finally{w.close();}
});
