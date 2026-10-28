const {test,expect,afterEach}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const UI=path.resolve(__dirname,'..','..','ui');
const read=(f)=>fs.readFileSync(path.join(UI,f),'utf8');

const envs=[];
afterEach(()=>{
 while(envs.length){ const env=envs.pop(); try{ expect(env.alerts).toEqual([]); } finally { env.w.close(); } }
});
const start=()=>{ const env=boot({noDialogs:true}); envs.push(env); return env; };

t('a feedback panel takes the Oat variant for its message type',()=>{
 const {w,d}=start();
 d.body.insertAdjacentHTML('beforeend','<div class="feedback-panel" data-feedback-context="x" role="alert"></div>');
 const panel=d.querySelector('.feedback-panel');
 const want={error:'danger',success:'success',warning:'warning'};
 Object.keys(want).forEach((type)=>{
  w.Feedback[type]('Said it',{context:'x',toast:false});
  expect(panel.getAttribute('data-variant')).toBe(want[type]);
  expect(panel.getAttribute('role')).toBe('alert');
  expect(panel.textContent).toContain('Said it');
 });
 w.Feedback.info('Said it',{context:'x',toast:false});
 expect(panel.hasAttribute('data-variant')).toBe(false);
 w.Feedback.error('Said it',{context:'x',toast:false});
 w.Feedback.clear('x');
 expect(panel.hasAttribute('data-variant')).toBe(false);
 w.Feedback.error('Said it',{context:'x',toast:false});
 w.Feedback.clearAll();
 expect(panel.hasAttribute('data-variant')).toBe(false);
});

test('the feedback panel colors come from the variant, not from our own rules',()=>{
 const css=read('blacksmith.css');
 expect(css).not.toMatch(/\.feedback-panel\.is-(info|error|success|warning)/);
 const base=css.slice(css.indexOf('\n.feedback-panel {'),css.indexOf('}',css.indexOf('\n.feedback-panel {')));
 expect(base).not.toMatch(/(^|[;{\s])(border|background|color|box-shadow):/);
});

// Alert text is the variant color on a solid fill, so its contrast depends on
// the theme tokens only. The percentage comes from the real stylesheet.
const lum=(hex)=>{ const c=[1,3,5].map(i=>parseInt(hex.slice(i,i+2),16)/255).map(v=>v<=0.03928?v/12.92:((v+0.055)/1.055)**2.4); return .2126*c[0]+.7152*c[1]+.0722*c[2]; };
const ratio=(a,b)=>{ const [x,y]=[lum(a),lum(b)].sort((p,q)=>q-p); return (x+.05)/(y+.05); };
const mix=(fg,bg,p)=>'#'+[1,3,5].map(i=>Math.round(parseInt(fg.slice(i,i+2),16)*p+parseInt(bg.slice(i,i+2),16)*(1-p)).toString(16).padStart(2,'0')).join('');
const tokens=(css,selector)=>{
 const start=css.indexOf('\n'+selector+' {');
 expect(start).toBeGreaterThan(-1);
 const out={}; css.slice(start,css.indexOf('}',start)).replace(/(--[a-z-]+): (#[0-9a-f]{6})/g,(m,k,v)=>{ out[k]=v; });
 return out;
};
test('alert text meets 4.5:1 for every variant in every theme',()=>{
 const css=read('forge-theme.css');
 const rule=css.match(/:is\(\[role="alert"\][^{]*\)\[data-variant\]:is\([^{]*\) \{([^}]*)\}/);
 expect(rule).not.toBeNull();
 const pct=Number(rule[1].match(/var\(--_variant-color\) (\d+)%, var\(--card\)/)[1])/100;
 [':root',':root[data-contrast="high"]','[data-theme="dark"]',':root[data-contrast="high"][data-theme="dark"]'].forEach((sel)=>{
  const t=tokens(css,sel);
  ['--success','--warning','--danger'].forEach((v)=>{
   expect(ratio(t[v],mix(t[v],t['--card'],pct))).toBeGreaterThanOrEqual(4.5);
  });
  expect(ratio(t['--foreground'],t['--background'])).toBeGreaterThanOrEqual(4.5);
 });
});
