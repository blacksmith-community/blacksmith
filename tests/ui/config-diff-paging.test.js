const {test,expect}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');

const open=async(ids,opts={})=>{
 const env=boot({oat:true,noDialogs:true,...opts});const {w,d}=env;
 w.__routes.push([/\/b\/configs\/cloud\/[^/]+\/versions/,(u,o,j)=>j(200,{configs:ids.map(id=>({id}))})]);
 w.__routes.push([/\/b\/configs\/cloud\/[^/]+\/diff/,(u,o,j)=>{
  const q=new URL(u,'http://x').searchParams;
  return j(200,{has_changes:true,diff_string:'changed '+q.get('from')+' to '+q.get('to')});
 }]);
 w.__routes.push([/\/b\/configs\/[^/]+$/,(u,o,j)=>j(200,{content:'c'})]);
 await w.showConfigVersions('cloud','alpha',ids[0]);await sleep(30);
 d.querySelector('.manifest-tab-btn[data-group="config-tabs"][data-tab="diffs"]').click();await sleep(30);
 return env;
};
const btn=(d,k)=>d.querySelector('#diff-pair-'+k);
const sel=d=>d.querySelector('#diff-version-selector');
const pick=async(w,d,v)=>{sel(d).value=v;sel(d).dispatchEvent(new w.Event('change'));await sleep(30);};
const press=async(d,k)=>{btn(d,k).click();await sleep(30);};
const state=d=>['first','prev','next','last'].map(k=>btn(d,k).disabled);

test('the row reads first, previous, select, next, last with names and keys',async()=>{
 const {w,d}=await open(['4','3','2','1']);try{
 const row=d.querySelector('.diff-controls');
 const ids=[...row.children].map(n=>n.id);
 expect(ids).toEqual(['diff-pair-first','diff-pair-prev','diff-version-selector','diff-pair-next','diff-pair-last']);
 expect(btn(d,'first').getAttribute('aria-label')).toBe('First version pair');
 expect(btn(d,'prev').getAttribute('aria-label')).toBe('Previous version pair');
 expect(btn(d,'next').getAttribute('aria-label')).toBe('Next version pair');
 expect(btn(d,'last').getAttribute('aria-label')).toBe('Last version pair');
 expect(sel(d).getAttribute('aria-label')).toBe('Compare versions');
 const keys=['first','prev','next','last'].map(k=>btn(d,k).dataset.keepKey);
 expect(keys.every(Boolean)).toBe(true);expect(new Set(keys).size).toBe(4);
 ['first','prev','next','last'].forEach(k=>{expect(btn(d,k).querySelector('svg')).not.toBeNull();expect(btn(d,k).textContent.trim()).toBe('');});
 }finally{w.close()}
});

test('the visible label is gone and the heading is bold with the new text',async()=>{
 const {w,d}=await open(['4','3','2','1']);try{
 expect(d.querySelector('label[for="diff-version-selector"]')).toBeNull();
 expect(d.querySelector('#config-diff-content').textContent).not.toContain('Compare versions:');
 await pick(w,d,'2|3');
 const h=d.querySelector('#diff-output .diff-output-heading');
 expect(h.tagName).toBe('STRONG');expect(h.textContent).toBe('Configuration Changes Diff: 2 → 3');
 expect(d.querySelector('#diff-output').textContent).toContain('changed 2 to 3');
 }finally{w.close()}
});

test('first is the oldest pair and last is the newest',async()=>{
 const {w,d}=await open(['4','3','2','1']);try{
 expect([...sel(d).options].map(o=>o.value)).toEqual(['','3|4','2|3','1|2']);
 await press(d,'first');expect(sel(d).value).toBe('1|2');
 await press(d,'last');expect(sel(d).value).toBe('3|4');
 }finally{w.close()}
});

test('previous steps to an older pair and next to a newer one',async()=>{
 const {w,d}=await open(['4','3','2','1']);try{
 await pick(w,d,'2|3');
 await press(d,'prev');expect(sel(d).value).toBe('1|2');
 await press(d,'next');expect(sel(d).value).toBe('2|3');
 await press(d,'next');expect(sel(d).value).toBe('3|4');
 }finally{w.close()}
});

test('buttons disable at each end',async()=>{
 const {w,d}=await open(['4','3','2','1']);try{
 await pick(w,d,'2|3');expect(state(d)).toEqual([false,false,false,false]);
 await pick(w,d,'1|2');expect(state(d)).toEqual([true,true,false,false]);
 await pick(w,d,'3|4');expect(state(d)).toEqual([false,false,true,true]);
 }finally{w.close()}
});

test('a button loads the same diff and state as the select',async()=>{
 const a=await open(['4','3','2','1']);const b=await open(['4','3','2','1']);try{
 await pick(a.w,a.d,'2|3');
 await press(b.d,'last');await press(b.d,'prev');await press(b.d,'prev');await press(b.d,'next');
 expect(sel(b.d).value).toBe('2|3');
 expect(b.d.querySelector('#diff-output').textContent).toBe(a.d.querySelector('#diff-output').textContent);
 expect(state(b.d)).toEqual(state(a.d));
 const req=c=>c.filter(x=>/\/diff\?/.test(x)).pop();
 expect(req(b.calls)).toBe(req(a.calls));
 expect(b.calls.some(x=>/^(POST|PUT|DELETE) /.test(x))).toBe(false);
 // the retained pair survives a reopen, as it does for a select choice
 b.w.hideConfigVersionsModal();await b.w.showConfigVersions('cloud','alpha','4');await sleep(40);
 expect(sel(b.d).value).toBe('2|3');
 }finally{a.w.close();b.w.close()}
});

test('focus stays on the button after the diff reloads',async()=>{
 const {w,d}=await open(['5','4','3','2','1']);try{
 await pick(w,d,'3|4');
 btn(d,'next').focus();btn(d,'next').click();await sleep(30);
 expect(sel(d).value).toBe('4|5');
 expect(btn(d,'next').disabled).toBe(true);expect(d.activeElement).toBe(sel(d));
 btn(d,'prev').focus();btn(d,'prev').click();await sleep(30);
 expect(sel(d).value).toBe('3|4');expect(d.activeElement).toBe(btn(d,'prev'));
 }finally{w.close()}
});

test('a button that disables under focus hands focus to the select',async()=>{
 const {w,d}=await open(['3','2','1']);try{
 await pick(w,d,'2|3');
 btn(d,'first').focus();btn(d,'first').click();await sleep(30);
 expect(sel(d).value).toBe('1|2');expect(btn(d,'first').disabled).toBe(true);
 expect(d.activeElement).toBe(sel(d));
 }finally{w.close()}
});

test('a single pair disables all four buttons and no pair leaves them off',async()=>{
 const one=await open(['2','1']);try{
 expect(state(one.d)).toEqual([true,true,true,true]);
 await pick(one.w,one.d,'1|2');expect(state(one.d)).toEqual([true,true,true,true]);
 }finally{one.w.close()}
 const none=await open(['1']);try{
 expect(state(none.d)).toEqual([true,true,true,true]);
 none.d.querySelector('#diff-pair-first').click();await sleep(10);
 expect(none.calls.some(x=>/\/diff\?/.test(x))).toBe(false);
 }finally{none.w.close()}
});

test('before a pair is chosen, previous and next start from the ends',async()=>{
 const {w,d}=await open(['4','3','2','1']);try{
 expect(sel(d).value).toBe('');expect(state(d)).toEqual([false,false,false,false]);
 await press(d,'next');expect(sel(d).value).toBe('3|4');
 }finally{w.close()}
});

test('the paging row stays on one line at phone width with tall targets',()=>{
 const fs=require('fs'),path=require('path');
 const css=fs.readFileSync(path.join(__dirname,'..','..','ui','blacksmith.css'),'utf8');
 expect(css).toMatch(/\.diff-controls\s*\{[^}]*flex-wrap:\s*nowrap/);
 expect(css).toMatch(/\.diff-controls \.diff-pair-btn\s*\{[^}]*min-height:\s*32px/);
 expect(css).toMatch(/\.diff-controls \.form-control\s*\{[^}]*min-width:\s*0/);
});

test('the chevron pair glyphs exist as icons',()=>{
 const {sources}=require('../../ui/icons.js');
 expect(Object.keys(sources)).toContain('chevrons-left');
 expect(Object.keys(sources)).toContain('chevrons-right');
});

test('the gap above the paging row is 8 to 12px',()=>{
 const fs=require('fs'),path=require('path');
 const css=fs.readFileSync(path.join(__dirname,'..','..','ui','blacksmith.css'),'utf8');
 const m=/#config-details-modal #diffs-panel\s*\{[^}]*padding-top:\s*(\d+)px/.exec(css);
 expect(m).not.toBeNull();expect(+m[1]).toBeGreaterThanOrEqual(8);expect(+m[1]).toBeLessThanOrEqual(12);
});
