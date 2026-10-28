const {test,expect}=require('bun:test');
const fs=require('fs');
const path=require('path');

const UI=path.resolve(__dirname,'..','..','ui');
const read=(f)=>fs.readFileSync(path.join(UI,f),'utf8');

// Every rule as {selectors, body}, with comments removed and each selector
// list split at its top-level commas, so ":is(a, b)" stays whole.
const parse=(css)=>{
 const out=[];
 css.replace(/\/\*[\s\S]*?\*\//g,'').replace(/([^{}]+)\{([^{}]*)\}/g,(m,sel,body)=>{
  const parts=[]; let depth=0, cur='';
  for(const ch of sel){
   if(ch==='('||ch==='[') depth++;
   if(ch===')'||ch===']') depth--;
   if(ch===','&&depth===0){ parts.push(cur); cur=''; } else cur+=ch;
  }
  parts.push(cur);
  out.push({selectors:parts.map((p)=>p.replace(/\s+/g,' ').trim()),body});
  return m;
 });
 return out;
};
const declares=(body,prop)=>new RegExp('(^|[;{\\s])'+prop+'\\s*:').test(body);

// A selector listed as gone has no rule left in its file. A selector listed
// with properties keeps its layout but none of those properties, because
// Oat styles them. Each entry names a selector that used to restyle a form
// control, so a later edit that brings one back fails here.
const GONE={
 'forge-theme.css':[
  '#upgrade input[type="radio"]',
  '.compact-configs-controls .checkbox-group input',
  '#blacksmith .compact-tasks-controls .checkbox-group input',
  '#blacksmith .compact-configs-controls .checkbox-group input',
 ],
 'blacksmith.css':[
  'select','select:hover:not(:disabled)','select:focus',
  'input','textarea','[data-theme="dark"] input','[data-theme="dark"] textarea',
  'input:focus','textarea:focus','[data-theme="dark"] input:focus','[data-theme="dark"] textarea:focus',
  'input::placeholder','textarea::placeholder',
  '.instance-group-select:hover','.job-select:hover',
  '[data-theme="dark"] .instance-group-select','[data-theme="dark"] .job-select',
  '[data-theme="dark"] .form-row input','[data-theme="dark"] .form-row textarea','[data-theme="dark"] .form-row select',
  '[data-theme="dark"] .connection-field select','[data-theme="dark"] .connection-field select:hover',
  '.org-space-table select:disabled',
  '[data-theme="dark"] .compact-tasks-controls .filter-group select','[data-theme="dark"] .compact-tasks-controls .filter-group select:hover',
  '.filter-input::placeholder',
  '.table-controls-container .log-file-dropdown:hover',
  '[data-theme="dark"] .table-controls-container .log-file-dropdown','[data-theme="dark"] .table-controls-container .log-file-dropdown:hover',
  '[data-theme="dark"] .diff-controls .form-control',
  '#services .filter-row .checkbox-label input[type="checkbox"]',
  '.checkbox-group input[type="checkbox"]','.compact-tasks-controls .checkbox-group input[type="checkbox"]','.compact-configs-controls .checkbox-group input[type="checkbox"]',
  '.checkbox-label input[type="checkbox"]',
 ],
};
const LOOK=['border','border-color','border-radius','background','background-color','color'];
const STRIPPED={
 'blacksmith.css':{
  'select:disabled':['background-color','border','border-color','color'],
  '.instance-group-select':LOOK.concat('cursor'),
  '.job-select':LOOK.concat('cursor'),
  '.form-row input':['background'],
  '.plugins-command-details .command-selector select':LOOK,
  '.plugins-table .argument-group input':['border-radius'],
  '.org-space-table select':LOOK,
  '.filter-group select':LOOK,
  '.compact-tasks-controls .filter-group select':LOOK.concat('cursor'),
  '.cert-fleet input[type="search"]':LOOK,
  '.form-group input[type="text"]':['border-radius'],
  '.table-controls-container .log-file-dropdown':LOOK.concat('cursor'),
  '.diff-controls .form-control':LOOK,
 },
 'forge-theme.css':{
  'select:where(:not([aria-invalid="true"]))':['background','border-radius'],
  'select[aria-invalid="true"]':['background','border-radius'],
  'input::placeholder':['color'],
  '#upgrade .rotate-operation-option input':['appearance','accent-color'],
  '.batch-release-picker .batch-release-selected-filter input':['appearance','accent-color'],
 },
 'console.css':{
  '#upgrade .upgrade-services input':LOOK,
  '#upgrade .upgrade-services select':LOOK,
 },
};

Object.entries(GONE).forEach(([file,sels])=>{
 test(file+' no longer restyles form controls Oat already styles',()=>{
  const all=parse(read(file));
  sels.forEach((sel)=>expect(all.filter((r)=>r.selectors.includes(sel)).length+' rule(s) for '+sel).toBe('0 rule(s) for '+sel));
 });
});
Object.entries(STRIPPED).forEach(([file,map])=>{
 test(file+' keeps layout on form controls and leaves their look to Oat',()=>{
  const all=parse(read(file));
  Object.entries(map).forEach(([sel,props])=>{
   const hits=all.filter((r)=>r.selectors.includes(sel));
   expect(hits.length>0?'present':sel+' is missing').toBe('present');
   hits.forEach((r)=>props.forEach((p)=>expect(declares(r.body,p)?sel+' sets '+p:'ok').toBe('ok')));
  });
 });
});

// The form row's full-width field rule would stretch a checkbox, and a
// checkbox otherwise takes Oat's size.
test('a checkbox in a form row is sized back to Oat, not stretched by the field rule',()=>{
 const hit=parse(read('blacksmith.css')).find((r)=>r.selectors.includes('.form-row .checkbox-label input[type="checkbox"]'));
 expect(hit).toBeTruthy();
 expect(hit.body).toMatch(/width:\s*1rem/);
 expect(hit.body).toMatch(/padding:\s*0/);
});

// The control fill must be a background-color, because the shorthand wipes
// the chevron Oat draws on a select as a background-image.
test('the forge control fill sets background-color so the select chevron survives',()=>{
 const hit=parse(read('forge-theme.css')).find((r)=>r.selectors.includes('select:where(:not([aria-invalid="true"]))'));
 expect(hit.body).toMatch(/background-color:\s*var\(--card\)/);
});

// Form controls take the theme's focus ring. The page-level 2px outlines
// that used to repeat it for inputs and selects are gone, while buttons,
// summaries, and links keep theirs.
const OUTLINE_GONE={
 'blacksmith.css':[
  '.dialog-modal-input:focus-visible','.dialog-modal-choice-option input:focus-visible',
  '.cert-fleet input:focus-visible','.cert-fleet select:focus-visible',
 ],
 'console.css':[
  'input:focus-visible','select:focus-visible',
  '#upgrade :is(button, input, select, summary):focus-visible',
 ],
};
Object.entries(OUTLINE_GONE).forEach(([file,sels])=>{
 test(file+' leaves form control focus to the theme ring',()=>{
  const all=parse(read(file));
  sels.forEach((sel)=>expect(all.filter((r)=>r.selectors.includes(sel)).length+' rule(s) for '+sel).toBe('0 rule(s) for '+sel));
 });
});
test('the theme focus ring still covers inputs, selects, and textareas',()=>{
 const hit=parse(read('forge-theme.css')).find((r)=>r.selectors.some((s)=>/^:is\(.*\binput\b.*\bselect\b.*\btextarea\b.*\):focus-visible$/.test(s)));
 expect(hit).toBeTruthy();
 expect(hit.body).toMatch(/outline:\s*3px/);
});

// Oat reserves padding-inline-end on a select for its chevron. A padding
// shorthand on a select resets that to the shorthand's own value, so the
// chevron then sits on top of the text. A select rule sets longhands for
// the sides it changes and leaves the end side alone.
test('no select rule resets the end padding Oat keeps for the chevron',()=>{
 const bad=[];
 ['blacksmith.css','console.css','forge-theme.css','view-guidance.css','routes.css'].forEach((file)=>{
  if(!fs.existsSync(path.join(UI,file))) return;
  parse(read(file)).forEach((r)=>r.selectors.forEach((s)=>{
   if(!/(^|[\s>+~])select(:[a-z-]+)*$/.test(s)&&!/^\.(instance-group|job)-select(:[a-z-]+)*$/.test(s)) return;
   if(declares(r.body,'padding')||declares(r.body,'padding-right')||declares(r.body,'padding-inline')||declares(r.body,'padding-inline-end')) bad.push(file+' '+s);
  }));
 });
 expect(bad).toEqual([]);
});
