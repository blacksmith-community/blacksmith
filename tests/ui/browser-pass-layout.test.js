const {test,expect}=require('bun:test');
const fs=require('fs');
const path=require('path');

const UI=path.resolve(__dirname,'..','..','ui');
const read=(f)=>fs.readFileSync(path.join(UI,f),'utf8');
const strip=(css)=>css.replace(/\/\*[\s\S]*?\*\//g,'');
// The body of every rule whose selector list contains exactly this selector.
const bodies=(file,selector)=>{
 const out=[];
 strip(read(file)).replace(/([^{}]+)\{([^{}]*)\}/g,(m,sel,body)=>{
  if(sel.split(',').map((s)=>s.replace(/\s+/g,' ').trim()).includes(selector)) out.push(body);
  return m;
 });
 return out.join(';');
};

// The bare header rule in blacksmith.css was written for the page header and
// also reaches the Appearance dialog's header, so the dialog resets it.
test('the Appearance dialog header is one plain row with no strip styling',()=>{
 const b=bodies('action-help.css','.theme-choice-dialog header');
 expect(b).toMatch(/flex-direction:\s*row/);
 expect(b).toMatch(/height:\s*auto/);
 expect(b).toMatch(/box-shadow:\s*none/);
 expect(b).toMatch(/position:\s*static/);
});
test('the Appearance dialog drops the padding Oat adds around its paragraph and list',()=>{
 const b=bodies('action-help.css','.theme-choice-dialog > div');
 expect(b).toMatch(/padding:\s*0/);
});