const {test,expect}=require('bun:test');
const fs=require('fs');
const path=require('path');

// The console's own scripts report real failures through console.warn and
// console.error, and leave no debug output behind. Several of the old
// console.log calls printed whole response objects.
const UI=path.resolve(__dirname,'..','..','ui');
const scripts=fs.readdirSync(UI).filter(f=>f.endsWith('.js')).sort();

test('the scan covers the console scripts and skips the vendored ones',()=>{
 expect(scripts).toContain('blacksmith.js');
 expect(scripts.every(f=>!f.includes('/'))).toBe(true);
});

scripts.forEach(file=>{
 test(`${file} has no console.log or console.debug calls`,()=>{
  const lines=fs.readFileSync(path.join(UI,file),'utf8').split('\n');
  const hits=[];
  lines.forEach((line,i)=>{ if(/\bconsole\s*\.\s*(log|debug)\s*\(/.test(line)) hits.push(`${file}:${i+1}`); });
  expect(hits).toEqual([]);
 });
});

// A console.warn or console.error call may pass string literals and a
// caught error's name, and nothing else, because anything else can carry
// certificate, credential, or connection data. The scan joins each call's
// arguments across lines. A call that passes more must appear in ALLOWED,
// keyed by its whitespace-normalized arguments, with the reason it is safe.
// Caught errors go through loggableError, which keeps the name, status, and
// code and never the message.
const ID='an identifier the operator already sees in the console, such as an instance, task, or manifest ID, and never a secret';
const KIND='a fixed internal name from the code, such as a tool, tab, or action name';
const ALLOWED={
 "`Failed to load ${path}:`, loggableError(err)":KIND+' (an API path)',
 "`Failed to ${verb} task:`, loggableError(err)":KIND,
 "'No original log text found for job:', jobKey":ID,
 "'Job not found in refreshed data:', jobKey":ID,
 "`Failed to load ${commandRunners[kind].tool} categories:`, loggableError(error)":KIND,
 "`Failed to load ${commandRunners[kind].tool} command details:`, loggableError(error)":KIND,
 "`Failed to load ${kind} history:`, loggableError(error)":KIND,
 "`Table ${tableSelector} not found`":'a CSS selector written in the code',
 "'Manifest text not found for ID:', manifestId":ID,
 "'Tab or panel not found:', tabId":KIND,
 "`Failed to fetch manifest for ${instanceId}:`, loggableError(error)":ID,
 "'Diff panels not found:', { leftPanel: !!leftPanel, rightPanel: !!rightPanel }":'two booleans',
 "`Failed to load ${tabType} content:`, loggableError(error)":KIND,
 "'Task raw text not found for ID:', taskId":ID,
 "'Invalid certificate data structure', { type: certToInspect === null ? 'null' : typeof certToInspect }":'the typeof of the value, never the value',
 "`Failed to load bindings for service ${serviceGuid}:`, loggableError(error)":ID,
 "'Resurrection toggle slider not found for:', instanceId":ID,
 "'Delete resurrection button not found for:', instanceId":ID,
};
const scanConsoleCalls=(src)=>{
 const found=[];const re=/\bconsole\s*\.\s*(warn|error)\s*\(/g;let m;
 while((m=re.exec(src))){
  let i=re.lastIndex,depth=1,q=null;const start=i;
  for(;i<src.length&&depth>0;i++){
   const c=src[i];
   if(q){ if(c==='\\') i++; else if(c===q) q=null; continue; }
   if(c==="'"||c==='"'||c==='`'){ q=c; continue; }
   if(c==='(') depth++; else if(c===')') depth--;
  }
  const args=src.slice(start,i-1).replace(/\s+/g,' ').trim();
  let rest=args
   .replace(/loggableError\(\s*[A-Za-z_$][\w$]*\s*\)/g,'')
   .replace(/'(?:[^'\\]|\\.)*'|"(?:[^"\\]|\\.)*"|`(?:[^`\\$]|\\.|\$(?!\{))*`/g,'')
   .replace(/\b([A-Za-z_$][\w$]*)\s*&&\s*\1\.name\b/g,'')
   .replace(/\b[A-Za-z_$][\w$]*(?:\.[A-Za-z_$][\w$]*)*\.name\b/g,'')
   .replace(/[\s,]/g,'');
  if(rest) found.push({line:src.slice(0,m.index).split('\n').length,args});
 }
 return found;
};

scripts.forEach(file=>{
 test(`${file} passes only string literals and error names to console.warn and console.error`,()=>{
  const bad=scanConsoleCalls(fs.readFileSync(path.join(UI,file),'utf8')).filter(h=>!(h.args in ALLOWED)).map(h=>`${file}:${h.line} ${h.args}`);
  expect(bad).toEqual([]);
 });
});

test('the scan sees multi-line calls, other variable names, and first arguments',()=>{
 expect(scanConsoleCalls("console.warn(\n  'bad',\n  cert\n);").length).toBe(1);
 expect(scanConsoleCalls("console.error(cert, 'bad');").length).toBe(1);
 expect(scanConsoleCalls("console.error('bad', certificateData);").length).toBe(1);
 expect(scanConsoleCalls("console.error('bad', e);").length).toBe(1);
 expect(scanConsoleCalls("console.error('bad', e && e.name);").length).toBe(0);
 expect(scanConsoleCalls("console.error('bad', loggableError(e));").length).toBe(0);
 expect(scanConsoleCalls("console.error('bad');").length).toBe(0);
});
