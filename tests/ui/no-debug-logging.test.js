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
 "`Failed to load ${path}:`, loggableError(err)":'the path is written in the code and never built from user input',
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
   .replace(/\b(?:e|err|error|[A-Za-z]*(?:Err|Error))\.name\b/g,'')
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
 expect(scanConsoleCalls("console.warn('bad', cert.subject.name);").length).toBe(1);
 expect(scanConsoleCalls("console.warn('bad', err.name);").length).toBe(0);
});

// loggableError is the one way a caught error reaches the console. The test
// runs its source as written in blacksmith.js.
const loadLoggableError=()=>{
 const src=fs.readFileSync(path.join(UI,'blacksmith.js'),'utf8');
 const start=src.indexOf('function loggableError(err) {');
 expect(start).toBeGreaterThan(-1);
 const end=src.indexOf('\n  }\n',start)+4;
 return new Function(src.slice(start,end)+'\nreturn loggableError;')();
};

test('loggableError keeps the HTTP status from a message that starts with it, and nothing else of the message',()=>{
 const log=loadLoggableError();
 expect(log(new Error('HTTP 404: secret-token-abc'))).toEqual({name:'Error',status:404,code:''});
 expect(log(new Error('HTTP 502'))).toEqual({name:'Error',status:502,code:''});
 expect(log(new Error('see HTTP 404 here')).status).toBe(0);
 expect(log(new Error('HTTP 4040')).status).toBe(0);
 expect(JSON.stringify(log(new Error('HTTP 500: password=hunter2')))).not.toContain('hunter2');
});

test('loggableError prefers a status property and passes a code only when it is a short slug',()=>{
 const log=loadLoggableError();
 const withStatus=Object.assign(new Error('HTTP 500: x'),{status:409,code:'instance_busy'});
 expect(log(withStatus)).toEqual({name:'Error',status:409,code:'instance_busy'});
 for(const bad of ['Has Spaces','UPPER','x'.repeat(65),'a;b','tok=abc123!','']) expect(log(Object.assign(new Error('x'),{code:bad})).code).toBe('');
 expect(log(Object.assign(new Error('x'),{code:'a'.repeat(64)})).code).toBe('a'.repeat(64));
 expect(log(null)).toEqual({name:'Error',status:0,code:''});
});
