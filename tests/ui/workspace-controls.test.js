const {test,expect}=require('bun:test');
test('main tabs end with batch operations and settings have an icon',()=>{
 const fs=require('fs');const {JSDOM}=require('./certificates/support.js');
 const d=new JSDOM(fs.readFileSync('ui/index.html','utf8')).window.document;
 expect([...d.querySelectorAll('.tabs > button')].map(x=>x.dataset.tab)).toEqual(['blacksmith','services','certificates','upgrade']);
 expect(!!d.querySelector('.batch-execution-settings summary svg[aria-hidden="true"]')).toBe(true);
});
