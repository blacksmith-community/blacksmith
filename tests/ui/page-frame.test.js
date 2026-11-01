const {test,expect}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot,sleep}}=require('./certificates/harness.js');

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

test('the page body renders no footer',async()=>{
 const {w,d}=boot();try{
 await sleep(50);
 expect(d.querySelector('body > footer')).toBeNull();
 }finally{w.close();}
});
test('the keyboard shortcuts dialog carries the copyright line and icon',async()=>{
 const {w,d}=boot();try{
 await sleep(50);
 const line=d.querySelector('#keyboard-help-dialog .keyboard-help-copyright');
 expect(line).not.toBeNull();
 expect(line.textContent).toContain('Copyright © 2026 Blacksmith Community');
 expect(line.querySelector('.keyboard-help-copyright-icon')).not.toBeNull();
 expect(bodies('forge-theme.css','.keyboard-help-copyright-icon')).toMatch(/url\("\/assets\/brand\/icon-192\.png"\)/);
 }finally{w.close();}
});
test('no stylesheet still styles the removed page footer',()=>{
 for(const f of ['blacksmith.css','console.css','forge-theme.css']){
  const css=strip(read(f));
  expect(css).not.toMatch(/(^\s*|[,}]\s*)(body\s*>\s*footer|footer)\s*[,{]/m);
 }
});

test('data table headers stay visible on an opaque surface token',()=>{
 const b=bodies('forge-theme.css','.main-content table thead th');
 expect(b).toMatch(/position:\s*sticky/);
 expect(b).toMatch(/top:\s*0/);
 expect(b).toMatch(/background:\s*var\(--secondary\)/);
 expect(b).toMatch(/box-shadow:\s*inset 0 -1px 0 var\(--border\)/);
 expect(bodies('forge-theme.css','.main-content table thead')).not.toMatch(/position:\s*sticky/);
});
test('no stylesheet pins the general table thead, and the overview scrolls inside its wrapper',()=>{
 for(const f of ['blacksmith.css','console.css','forge-theme.css']){
  const css=strip(read(f));
  for(const m of css.matchAll(/([^{}]*)\{([^}]*)\}/g)){
   const general=m[1].split(',').some(sel=>/^\s*(\.main-content\s+)?table\s+thead\s*$/.test(sel));
   if(general) expect(m[2]).not.toMatch(/position:\s*sticky/);
  }
 }
 const frame=bodies('blacksmith.css','.plans-overview')+bodies('blacksmith.css','.instances-overview');
 expect(frame).toMatch(/display:\s*flex/);
 expect(frame).toMatch(/flex-direction:\s*column/);
 expect(frame).toMatch(/overflow:\s*hidden/);
 const scroll=bodies('blacksmith.css','.plans-overview-scroll')+bodies('blacksmith.css','.instances-overview-scroll');
 expect(scroll).toMatch(/flex:\s*1/);
 expect(scroll).toMatch(/min-height:\s*0/);
 expect(scroll).toMatch(/overflow:\s*auto/);
});
test('the header surface token is opaque in dark and high contrast',()=>{
 const css=read('forge-theme.css');
 for(const sel of [':root','[data-theme="dark"]',':root[data-contrast="high"]',':root[data-contrast="high"][data-theme="dark"]']){
  const i=css.indexOf(sel+' {');
  expect(i).toBeGreaterThanOrEqual(0);
  const block=css.slice(i,css.indexOf('}',i));
  expect(block).toMatch(/--secondary:\s*(#[0-9a-f]{6}|var\(--surface-raised\))\s*;/i);
 }
});
