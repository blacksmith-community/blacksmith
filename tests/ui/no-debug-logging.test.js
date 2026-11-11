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

// A console call never receives a certificate data object, because that
// object carries certificate contents.
scripts.forEach(file=>{
 test(`${file} passes no certificate data to console calls`,()=>{
  const lines=fs.readFileSync(path.join(UI,file),'utf8').split('\n');
  const hits=[];
  lines.forEach((line,i)=>{
   if(/\bconsole\s*\.\s*(warn|error)\s*\(.*,\s*(certificateData|certToInspect|certData)\b/.test(line)) hits.push(`${file}:${i+1}`);
  });
  expect(hits).toEqual([]);
 });
});
