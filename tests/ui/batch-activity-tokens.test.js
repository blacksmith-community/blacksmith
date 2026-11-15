const {test,expect}=require('bun:test');
const fs=require('fs'),path=require('path');
const css=fs.readFileSync(path.join(__dirname,'../../ui/blacksmith.css'),'utf8');
// Rules for Batch Activity, the "#upgrade" scoped block of status colours.
const rules=[...css.matchAll(/(^|\n)((?:\[data-theme="dark"\] )?#upgrade [^{}]*)\{([^}]*)\}/g)].map(m=>({selector:m[2].trim(),body:m[3]}));
const colourRules=rules.filter(r=>/upgrade-task-stats|status-icon|task-detail-summary|upgrade-task-instance|job-control-btn|status-cancelled|instance-cancel-btn/.test(r.selector));
test('Batch Activity status colours come from theme tokens',()=>{
 expect(colourRules.length).toBeGreaterThan(10);
 const bad=colourRules.filter(r=>/#(10b981|ef4444|059669|dc2626)\b/i.test(r.body)).map(r=>r.selector);
 expect(bad).toEqual([]);
});
test('Batch Activity filled status surfaces pair a token background with its foreground token',()=>{
 for(const r of colourRules){
  const bg=r.body.match(/background(?:-color)?:\s*var\(--(success|danger)\)/);
  if(!bg)continue;
  expect(r.body).toContain('color: var(--'+bg[1]+'-foreground)');
 }
});
