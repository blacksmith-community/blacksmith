const {test,expect}=require('bun:test');
const fs=require('fs');
const path=require('path');

const UI=path.resolve(__dirname,'..','..','ui');
const read=(f)=>fs.readFileSync(path.join(UI,f),'utf8');

test('badges use Oat badge with a data-variant and no badge-color class',()=>{
 const js=read('blacksmith.js');
 expect(js).not.toMatch(/class="badge badge-/);
 expect(js).toContain("active: ['success', 'Active']");
 expect(js).toContain("inactive: ['neutral', 'Inactive']");
 expect(js).toContain("error: ['error', 'Error']");
});
test('log levels and config types use Oat badge variants',()=>{
 const js=read('blacksmith.js');
 expect(js).not.toContain('level-badge');
 expect(js).not.toContain('config-type-badge');
 expect(js).toContain("const LOG_LEVEL_VARIANTS = { error: 'danger', warning: 'warning', warn: 'warning', debug: 'secondary' };");
 expect(js).not.toContain('CONFIG_TYPE_VARIANTS');
 expect(js).toContain('<span class="badge outline" data-config-type=');
 for (const f of ['blacksmith.css','forge-theme.css']) {
  expect(read(f)).not.toMatch(/level-badge|config-type-badge/);
 }
});
test('our stylesheet no longer redefines Oat badge',()=>{
 const css=read('blacksmith.css');
 expect(css).not.toMatch(/^\.badge\s*\{/m);
 expect(css).not.toMatch(/\.badge-(success|secondary|warning)/);
});

// Badge text must reach 4.5:1 against its fill in every theme. The fill is
// the variant color mixed into the card color, read from the real stylesheet.
const lum=(hex)=>{ const c=[1,3,5].map(i=>parseInt(hex.slice(i,i+2),16)/255).map(v=>v<=0.03928?v/12.92:((v+0.055)/1.055)**2.4); return .2126*c[0]+.7152*c[1]+.0722*c[2]; };
const ratio=(a,b)=>{ const [x,y]=[lum(a),lum(b)].sort((p,q)=>q-p); return (x+.05)/(y+.05); };
const mix=(fg,bg,p)=>'#'+[1,3,5].map(i=>Math.round(parseInt(fg.slice(i,i+2),16)*p+parseInt(bg.slice(i,i+2),16)*(1-p)).toString(16).padStart(2,'0')).join('');
const tokens=(css,selector)=>{
 const start=css.indexOf('\n'+selector+' {');
 expect(start).toBeGreaterThan(-1);
 const out={}; css.slice(start,css.indexOf('}',start)).replace(/(--[a-z-]+): (#[0-9a-f]{6}|var\(--[a-z-]+\))/g,(m,k,v)=>{ out[k]=v; }); for(const k of Object.keys(out)){ const m=out[k].match(/^var\((--[a-z-]+)\)$/); if(m) out[k]=out[m[1]]; }
 return out;
};
test('badge text meets 4.5:1 in every theme',()=>{
 const css=read('forge-theme.css');
 const rule=css.match(/\.badge:is\(\[data-variant="success"\], \[data-variant="warning"\], \[data-variant="danger"\]\) \{([^}]*)\}/);
 expect(rule).not.toBeNull();
 const pct=Number(rule[1].match(/var\(--_variant-color\) (\d+)%, var\(--card\)/)[1])/100;
 const themes=[':root',':root[data-contrast="high"]','[data-theme="dark"]',':root[data-contrast="high"][data-theme="dark"]'];
 themes.forEach((sel)=>{
  const t=tokens(css,sel);
  ['--success','--warning','--danger'].forEach((v)=>{
   expect(ratio(t[v],mix(t[v],t['--card'],pct))).toBeGreaterThanOrEqual(4.5);
  });
  expect(ratio(t['--secondary-foreground'],t['--secondary'])).toBeGreaterThanOrEqual(4.5);
  expect(ratio(t['--primary-foreground'],t['--primary'])).toBeGreaterThanOrEqual(4.5);
 });
});
