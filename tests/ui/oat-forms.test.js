const {test,expect}=require('bun:test');
const fs=require('fs');
const path=require('path');

const UI=path.resolve(__dirname,'..','..','ui');
const read=(f)=>fs.readFileSync(path.join(UI,f),'utf8');

test('the task progress bar is Oat progress, with only status colors from us',()=>{
 const css=read('blacksmith.css');
 expect(css).not.toContain('upgrade-task-progress-bar');
 const i=css.indexOf('#upgrade .upgrade-task-progress {');
 expect(i).toBeGreaterThan(-1);
 expect(css.slice(i,css.indexOf('}',i))).not.toMatch(/(^|[;{\s])(height|background|background-color|border-radius|overflow):/);
});

// A control rule of ours may size and place a control. It may not repeat
// Oat's border, fill, text color, or focus treatment, and none may turn the
// outline off, because that cancels the focus ring from forge-theme.css.
const block=(css,selector)=>{
 const i=css.indexOf('\n'+selector+' {');
 expect(i).toBeGreaterThan(-1);
 return css.slice(i,css.indexOf('}',i));
};
const hasBlock=(css,selector)=>css.includes('\n'+selector+' {');
const REPEATS=/(^|[;{\s])(border|border-color|border-radius|background|background-color|color|box-shadow|outline|transition):/;

test('completed and failed task progress recolor Oat through its fill token',()=>{
 const css=read('blacksmith.css');
 ['completed','failed'].forEach((st)=>expect(block(css,'#upgrade .upgrade-task-item.status-'+st+' .upgrade-task-progress')).toMatch(/--primary:\s*var\(--/));
});

test('upgrade filter controls keep their sizes and leave the look to Oat',()=>{
 const css=read('blacksmith.css');
 ['#upgrade .upgrade-filters select','#upgrade .upgrade-filters input[type="number"]','#upgrade .upgrade-range-selection input[type="number"]']
  .forEach((sel)=>expect(block(css,sel)).not.toMatch(REPEATS));
 ['#upgrade .upgrade-filters select:focus','#upgrade .upgrade-filters select:disabled','#upgrade .upgrade-filters input[type="number"]:focus','#upgrade .upgrade-filters input[type="number"]::placeholder','#upgrade .upgrade-range-selection input[type="number"]:focus']
  .forEach((sel)=>expect(hasBlock(css,sel)).toBe(false));
});

test('the settings and dialog text fields keep their size and font and leave the look to Oat',()=>{
 const css=read('blacksmith.css');
 expect(block(css,'.settings-input')).not.toMatch(REPEATS);
 expect(hasBlock(css,'.settings-input:focus')).toBe(false);
 expect(block(css,'.dialog-modal-input')).not.toMatch(REPEATS);
 expect(hasBlock(css,'.dialog-modal-input:focus')).toBe(false);
 expect(hasBlock(css,'[data-theme="dark"] .dialog-modal-input')).toBe(false);
});

test('the certificate settings number fields keep their size and leave the look to Oat',()=>{
 const css=read('blacksmith.css');
 expect(block(css,'.cert-settings-field input[type="number"]')).not.toMatch(REPEATS);
 expect(hasBlock(css,'.cert-settings-field input[aria-invalid="true"]')).toBe(false);
 expect(hasBlock(css,'.cert-settings-field input:focus-visible')).toBe(false);
});

// Control text needs 4.5:1 and a control border needs 3:1 (WCAG 1.4.11),
// against the surfaces the controls sit on, in all four themes.
const lum=(hex)=>{ const c=[1,3,5].map(i=>parseInt(hex.slice(i,i+2),16)/255).map(v=>v<=0.03928?v/12.92:((v+0.055)/1.055)**2.4); return .2126*c[0]+.7152*c[1]+.0722*c[2]; };
const ratio=(a,b)=>{ const [x,y]=[lum(a),lum(b)].sort((p,q)=>q-p); return (x+.05)/(y+.05); };
test('form control text, placeholder, border, and focus ring meet contrast in every theme',()=>{
 const css=read('forge-theme.css');
 [':root',':root[data-contrast="high"]','[data-theme="dark"]',':root[data-contrast="high"][data-theme="dark"]'].forEach((sel)=>{
  const start=css.indexOf('\n'+sel+' {');
  expect(start).toBeGreaterThan(-1);
  const t={}; css.slice(start,css.indexOf('}',start)).replace(/(--[a-z-]+): (#[0-9a-f]{6}|var\(--[a-z-]+\))/g,(m,k,v)=>{ t[k]=v; }); for(const k of Object.keys(t)){ const m=t[k].match(/^var\((--[a-z-]+)\)$/); if(m) t[k]=t[m[1]]; }
  ['--card','--background'].forEach((surface)=>{
   expect(ratio(t['--foreground'],t[surface])).toBeGreaterThanOrEqual(4.5);
   expect(ratio(t['--muted-foreground'],t[surface])).toBeGreaterThanOrEqual(4.5);
   expect(ratio(t['--input'],t[surface])).toBeGreaterThanOrEqual(3);
   expect(ratio(t['--ring'],t[surface])).toBeGreaterThanOrEqual(3);
  });
 });
});

// Our stylesheets have no cascade layer, so any rule of ours that sets a
// control's border beats Oat's layered invalid-state border whatever its
// specificity. The border rule must step aside for an invalid control, and
// a rule of ours must give the invalid control the danger color. This reads
// the rules; it cannot compute the cascade, so it cannot catch a new rule
// elsewhere with a higher specificity.
const rules=(css)=>{ const out=[]; css.replace(/([^{}]+)\{([^{}]*)\}/g,(m,sel,body)=>{ out.push({sel:sel.trim(),body}); return m; }); return out; };
test('an invalid control keeps the danger border over our control border rule',()=>{
 const all=rules(read('forge-theme.css'));
 const border=all.find((r)=>/^input:not\(\[type="checkbox"\]\)/.test(r.sel)&&/(^|[;\s])border:/.test(r.body));
 expect(border).toBeTruthy();
 expect(border.sel).toContain('aria-invalid');
 const invalid=all.find((r)=>/aria-invalid="true"/.test(r.sel)&&!/:where\(/.test(r.sel)&&/border-color:\s*var\(--danger\)/.test(r.body));
 expect(invalid).toBeTruthy();
});

test('our control border uses the same token the contrast test checks',()=>{
 const border=rules(read('forge-theme.css')).find((r)=>/^input:not\(\[type="checkbox"\]\)/.test(r.sel)&&/(^|[;\s])border:/.test(r.body));
 expect(border.body).toMatch(/border:\s*1px solid var\(--input\)/);
 expect(border.body).toMatch(/color:\s*var\(--foreground\)/);
});

// Cached clients mix old styles with new markup when a changed asset keeps
// its old key, so every asset changed in the Oat work carries its current key.
test('the assets changed by the Oat form work carry their cache keys',()=>{
 const html=read('index.html');
 [['blacksmith.css','20261112-oat-393'],['forge-theme.css','20261112-oat-354'],['blacksmith.js','20261112-oat-411']].forEach(([f,k])=>expect(html).toContain('/'+f+'?v='+k+'"'));
});

// tests/ui/asset-keys.json pins each keyed asset's key and content hash. An
// asset whose content differs from its pin must take a new key in
// index.html, and the pin is then updated with it. This needs no git, but
// it cannot tell a pin refreshed without a key bump from a correct update.
test('every keyed asset whose content changed has a new cache key',()=>{
 const crypto=require('crypto');
 const pins=JSON.parse(read('asset-keys.json'.replace(/^/,'../tests/ui/')));
 const html=read('index.html');
 const seen=new Set();
 html.replace(/(?:href|src)="\/([^"?]+)\?v=([^"]+)"/g,(m,f,k)=>{
  seen.add(f);
  const pin=pins[f];
  expect(pin ? 'pinned' : f+' is not pinned').toBe('pinned');
  const sha=crypto.createHash('sha256').update(fs.readFileSync(path.join(UI,f))).digest('hex');
  if(sha!==pin.sha256&&k===pin.key) expect(f+' changed but its key is still '+k).toBe(f+' changed with a new key');
  expect(k+(sha===pin.sha256?'':' with changed content')).toBe(pin.key+(sha===pin.sha256?'':' with changed content'));
  return m;
 });
 expect(Object.keys(pins).sort()).toEqual([...seen].sort());
});

// The invalid exclusion must add no specificity, or the valid control rule
// outranks the earlier select and textarea rules it used to lose to, such as
// select:focus. Specificity here is (ids, classes/attributes/pseudo-classes,
// elements) of the selector with :where() removed and :not() read as its
// argument.
const specificity=(sel)=>{
 let s=sel.replace(/:where\((?:[^()]|\([^()]*\))*\)/g,'');
 s=s.replace(/:not\(([^()]*)\)/g,'$1');
 const ids=(s.match(/#[\w-]+/g)||[]).length;
 const cls=(s.match(/\.[\w-]+|\[[^\]]*\]|:(?!:)[\w-]+/g)||[]).length;
 const el=(s.replace(/\[[^\]]*\]/g,'').match(/(^|[\s>+~])[a-z][\w-]*/g)||[]).length;
 return [ids,cls,el].join(',');
};
test('the invalid exclusion on the control border rule adds no specificity',()=>{
 const rule=rules(read('forge-theme.css')).find((r)=>/^input:not\(\[type="checkbox"\]\)/.test(r.sel)&&/(^|[;\s])border:/.test(r.body));
 const parts=rule.sel.split(/,(?![^(]*\))/).map((p)=>p.trim());
 expect(parts.length).toBe(3);
 parts.forEach((part)=>{
  const bare=part.replace(/:not\(\[aria-invalid="true"\]\)/g,'');
  expect(part).toContain('aria-invalid');
  expect(specificity(part)).toBe(specificity(bare));
 });
 expect(specificity(parts[1])).toBe('0,0,1');
 expect(specificity(parts[2])).toBe('0,0,1');
});

test('the invalid border rule skips checkboxes and radios like the valid rule',()=>{
 const rule=rules(read('forge-theme.css')).find((r)=>/aria-invalid="true"\]/.test(r.sel)&&/border-color:\s*var\(--danger\)/.test(r.body)&&!/\bwhere\b/.test(r.sel));
 expect(rule.sel).toContain('input:not([type="checkbox"]):not([type="radio"])[aria-invalid="true"]');
});
