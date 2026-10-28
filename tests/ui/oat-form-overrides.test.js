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
 'blacksmith.css':[
  'select','select:hover:not(:disabled)','select:focus',
  'input','textarea','[data-theme="dark"] input','[data-theme="dark"] textarea',
  'input:focus','textarea:focus','[data-theme="dark"] input:focus','[data-theme="dark"] textarea:focus',
  'input::placeholder','textarea::placeholder',
 ],
};
const STRIPPED={
 'blacksmith.css':{
  'select:disabled':['background-color','border','border-color','color'],
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
