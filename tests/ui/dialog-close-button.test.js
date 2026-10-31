const {test,expect}=require('bun:test');
const fs=require('fs');
const path=require('path');

const UI=path.resolve(__dirname,'..','..','ui');
const read=(f)=>fs.readFileSync(path.join(UI,f),'utf8');
const strip=(css)=>css.replace(/\/\*[\s\S]*?\*\//g,'');
const bodies=(file,selector)=>{
 const out=[];
 strip(read(file)).replace(/([^{}]+)\{([^{}]*)\}/g,(m,sel,body)=>{
  if(sel.split(',').map((s)=>s.replace(/\s+/g,' ').trim()).includes(selector)) out.push(body);
  return m;
 });
 return out.join(';');
};

// Quick Navigation, Keyboard shortcuts, and Appearance each drew their own
// close button, so they share one class and one rule.
test('the three dialogs mark their close button with the shared class',()=>{
 const html=read('index.html');
 for(const id of ['keyboard-help-dialog','navigation-dialog']){
  const dialog=html.slice(html.indexOf('<dialog id="'+id+'"'));
  const button=dialog.match(/<button[^>]*data-close[^>]*>/)[0];
  expect(button).toMatch(/class="dialog-close"/);
  expect(dialog.slice(0,dialog.indexOf('</div>'))).toMatch(/width="18" height="18"/);
 }
 expect(html).toContain('aria-label="Close keyboard shortcuts"');
 expect(html).toContain('aria-label="Close navigation"');
 const js=read('action-help.js');
 expect(js).toMatch(/<button type="button" class="dialog-close" data-close aria-label="Close appearance"/);
 expect(js).toMatch(/blacksmithIcon\('x', \{ size: 18 \}\)/);
});
test('one rule draws the outlined close button',()=>{
 const b=bodies('forge-theme.css','dialog button.dialog-close');
 expect(b).toMatch(/width:\s*36px/);
 expect(b).toMatch(/height:\s*36px/);
 expect(b).toMatch(/border:\s*1px solid var\(--border-primary\)/);
 expect(b).toMatch(/background:\s*var\(--bg-tertiary\)/);
 expect(b).toMatch(/color:\s*var\(--text-secondary\)/);
 expect(b).toMatch(/border-radius:\s*5px/);
});
test('no dialog restyles its own close button',()=>{
 expect(bodies('forge-theme.css','#navigation-dialog [data-close]')).toBe('');
 expect(bodies('forge-theme.css','#keyboard-help-dialog [data-close]')).toBe('');
 expect(bodies('action-help.css','.theme-choice-dialog [data-close]')).toBe('');
 expect(bodies('console.css','.navigation-heading button')).toBe('');
});
