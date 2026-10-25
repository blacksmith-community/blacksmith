const {test,expect}=require('bun:test');
test('main tabs end with batch operations and settings have an icon',()=>{
 const fs=require('fs');const {JSDOM}=require('./certificates/support.js');
 const d=new JSDOM(fs.readFileSync('ui/index.html','utf8')).window.document;
 expect([...d.querySelectorAll('.tabs > button')].map(x=>x.dataset.tab)).toEqual(['blacksmith','services','certificates','upgrade']);
 expect(!!d.querySelector('.batch-execution-settings summary svg[aria-hidden="true"]')).toBe(true);
});

test('theme actions persist explicit modes and restore system preference',()=>{
 const {main:{boot}}=require('./certificates/harness.js');const {w,d}=boot();
 try {
  w.blacksmithTheme.setPreference('dark');
  expect(d.documentElement.dataset.theme).toBe('dark');
  expect(w.localStorage.getItem('theme')).toBe('dark');
  w.blacksmithTheme.setPreference('light');
  expect(d.documentElement.hasAttribute('data-theme')).toBe(false);
  expect(w.localStorage.getItem('theme')).toBe('light');
  w.blacksmithTheme.setPreference('system');
  expect(w.localStorage.getItem('theme')).toBeNull();
 } finally {w.close();}
});
