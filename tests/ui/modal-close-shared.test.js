const {test,expect}=require('bun:test');
const fs=require('fs');
const path=require('path');

const UI=path.resolve(__dirname,'..','..','ui');
const strip=(css)=>css.replace(/\/\*[\s\S]*?\*\//g,'');
// Every rule of a stylesheet as its trimmed selectors and its body.
const rules=(file)=>{
 const out=[];
 strip(fs.readFileSync(path.join(UI,file),'utf8')).replace(/([^{}]+)\{([^{}]*)\}/g,(m,sel,body)=>{
  out.push({selectors:sel.split(',').map((s)=>s.replace(/\s+/g,' ').trim()),body});
  return m;
 });
 return out;
};

test('the modal close buttons take the one shared close button rule',()=>{
 const shared=rules('forge-theme.css').filter((r)=>r.selectors.includes('dialog button.dialog-close'));
 expect(shared.length).toBe(1);
 expect(shared[0].selectors).toContain('dialog button.modal-close');
 const hover=rules('forge-theme.css').find((r)=>r.selectors.includes('dialog button.dialog-close:hover'));
 expect(hover.selectors).toContain('dialog button.modal-close:hover');
});

test('the declarations are not copied for the modal close buttons',()=>{
 for(const file of ['blacksmith.css','forge-theme.css']){
  for(const r of rules(file)){
   for(const sel of r.selectors){
    if(sel==='dialog button.modal-close'||sel==='dialog button.modal-close:hover') continue;
    expect(sel).not.toMatch(/\.modal-close/);
   }
  }
 }
});

test('each modal close button keeps its label and handler',()=>{
 const js=fs.readFileSync(path.join(UI,'blacksmith.js'),'utf8');
 expect((js.match(/class="modal-close" aria-label="Close modal"/g)||[]).length).toBeGreaterThanOrEqual(2);
 expect(js).toMatch(/querySelector\('\.modal-close'\)\.addEventListener\('click', \(\) => window\.hideTaskDetailsModal\(\)\)/);
});
