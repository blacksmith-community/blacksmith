const {test,expect}=require('bun:test');
test('rail areas end with batch operations and settings have an icon',()=>{
 const fs=require('fs');const {JSDOM}=require('./certificates/support.js');const {watchUncaught}=require('./watch-uncaught.js');
 const d=watchUncaught(new JSDOM(fs.readFileSync('ui/index.html','utf8')).window).document;
 expect([...d.querySelectorAll('#console-rail .rail-areas > [data-area]')].map(x=>x.dataset.area)).toEqual(['blacksmith','services','certificates','upgrade']);
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

test('high contrast themes retain dark styling and clear on return to Anvil',()=>{
 const {main:{boot}}=require('./certificates/harness.js');const {w,d}=boot();
 try{
 for(const mode of ['high-contrast-dark','high-contrast-light']){
  w.blacksmithTheme.setPreference(mode);
  expect(d.documentElement.getAttribute('data-contrast')).toBe('high');
  expect(d.documentElement.style.colorScheme).toBe(mode.endsWith('dark')?'dark':'light');
  expect(w.localStorage.getItem('theme')).toBe(mode);
 }
 w.blacksmithTheme.setPreference('dark');
 expect(d.documentElement.hasAttribute('data-contrast')).toBe(false);
 expect(d.getElementById('themeText').textContent).toBe('Anvil Dark');
 expect(d.getElementById('themeToggle').parentElement.lastElementChild.id).toBe('themeToggle');
 }finally{w.close();}
});
