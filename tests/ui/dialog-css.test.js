const {test,expect}=require('bun:test');
const fs=require('fs');
const path=require('path');

const UI=path.resolve(__dirname,'..','..','ui');
const rule=(file,selector)=>{
 const css=fs.readFileSync(path.join(UI,file),'utf8');
 const start=css.indexOf('\n'+selector+' {');
 expect(start).toBeGreaterThan(-1);
 return css.slice(start,css.indexOf('}',start));
};

// Oat's dialog already draws a one pixel border in --border, fills the box
// with --card, and removes the padding, so these dialogs restate none of it.
test('the keyboard help dialog leaves border, background, color, and padding to Oat',()=>{
 const body=rule('forge-theme.css','#keyboard-help-dialog');
 expect(body).not.toMatch(/(^|[;{\s])border:/);
 expect(body).not.toMatch(/(^|[;{\s])background:/);
 expect(body).not.toMatch(/(^|[;{\s])padding:/);
 expect(body).not.toMatch(/(^|[;{\s])color:/);
 expect(body).toMatch(/border-radius: 12px/);
});
test('the theme dialog leaves border, background, and color to Oat',()=>{
 const body=rule('action-help.css','.theme-choice-dialog');
 expect(body).not.toMatch(/(^|[;{\s])border:/);
 expect(body).not.toMatch(/(^|[;{\s])background:/);
 expect(body).not.toMatch(/(^|[;{\s])color:/);
 expect(body).toMatch(/padding: 16px/);
});
