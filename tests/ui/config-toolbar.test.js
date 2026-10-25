const {test,expect}=require('bun:test');
const {main:{boot}}=require('./certificates/harness.js');
test('config metadata and named actions live in one toolbar',()=>{
 const {w,d}=boot();try{
 const host=d.createElement('div');host.innerHTML=w.__T.formatConfigs({configs:[{id:'1',type:'cloud',name:'default',created_at:'2026-10-25'}]});
 const toolbar=host.querySelector('.compact-configs-controls');expect(!!toolbar.querySelector('.configs-summary')).toBe(true);
 expect(!!host.querySelector('.configs-note')).toBe(false);
 expect(toolbar.querySelector('.configs-help').textContent).toContain('* Active');
 expect(toolbar.querySelectorAll('input[type="checkbox"]').length).toBe(4);
 expect(toolbar.querySelector('input[value="cloud"]').parentElement.textContent).toContain('Cloud (1)');
 expect(toolbar.querySelector('#refresh-configs-btn').getAttribute('aria-label')).toBe('Refresh configs');
 }finally{w.close();}
});
