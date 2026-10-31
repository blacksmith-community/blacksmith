const {test,expect}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const UI=path.resolve(__dirname,'..','..','ui');
const js=fs.readFileSync(path.join(UI,'blacksmith.js'),'utf8');
const css=['blacksmith.css','forge-theme.css'].map((f)=>fs.readFileSync(path.join(UI,f),'utf8')).join('\n').replace(/\/\*[\s\S]*?\*\//g,'');

t('one em dash constant stands for every empty value',async()=>{
 const {w}=boot();await sleep(50);try{
 expect(w.__T.EMPTY).toBe('—');
 expect(w.__T.EMPTY_HTML).toBe('<span class="empty-value">—</span>');
 for(const v of [null,undefined,'','-','—']) expect(w.__T.isEmptyValue(v)).toBe(true);
 for(const v of ['n/a','None','Not reported','a-b',0]) expect(w.__T.isEmptyValue(v)).toBe(false);
 }finally{w.close();}
});
t('a VM table cell with no value shows the muted em dash',async()=>{
 const {w,d}=boot();await sleep(50);try{
 const host=d.createElement('div');d.body.appendChild(host);
 host.innerHTML=w.__T.formatVMs([{id:'abc',job_name:'blacksmith',index:0,state:'running',job_state:'running'}],'inst-1');
 const cells=[...host.querySelectorAll('tbody td')].map((td)=>td.textContent.trim());
 expect(cells).toContain('—');
 expect(cells).not.toContain('-');
 expect(host.querySelectorAll('tbody td .empty-value').length).toBeGreaterThan(0);
 }finally{w.close();}
});
t('setCellText writes the em dash for an empty value and the text otherwise',async()=>{
 const {w,d}=boot();await sleep(50);try{
 const el=d.createElement('span');
 w.__T.setCellText(el,'');
 expect(el.innerHTML).toBe('<span class="empty-value">—</span>');
 w.__T.setCellText(el,'-');
 expect(el.textContent).toBe('—');
 w.__T.setCellText(el,'<b>x</b>');
 expect(el.textContent).toBe('<b>x</b>');
 expect(el.querySelector('b')).toBeNull();
 }finally{w.close();}
});
t('the empty value is drawn in the muted text color',()=>{
 const m=css.match(/\.empty-value\s*\{([^{}]*)\}/);
 expect(m).not.toBeNull();
 expect(m[1]).toMatch(/color:\s*var\(--(text-muted|muted-foreground)\)/);
});
t('no source site still writes a bare hyphen for a missing value',()=>{
 const stray=js.split('\n').filter((l)=>/'-'/.test(l))
  .filter((l)=>!/replace\(|split\('-'\)|isEmptyValue = |!== '-'|^\s*\/\//.test(l));
 expect(stray).toEqual([]);
 expect(js).not.toMatch(/'–'/);
});
