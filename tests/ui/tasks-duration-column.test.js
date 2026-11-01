const {test,expect}=require('bun:test');
const fs=require('fs');
const path=require('path');

const UI=path.resolve(__dirname,'..','..','ui');
const strip=(css)=>css.replace(/\/\*[\s\S]*?\*\//g,'');
// The body of every rule whose selector list contains exactly this selector,
// across the stylesheets in the order the page loads them.
const bodies=(selector)=>{
 const out=[];
 for(const file of ['blacksmith.css','forge-theme.css']){
  strip(fs.readFileSync(path.join(UI,file),'utf8')).replace(/([^{}]+)\{([^{}]*)\}/g,(m,sel,body)=>{
   if(sel.split(',').map((s)=>s.replace(/\s+/g,' ').trim()).includes(selector)) out.push(body);
   return m;
  });
 }
 return out.join(';');
};

// The Duration cell was a gray monospace value that wrapped inside the column.
test('the Duration cell uses the table text face and color on one line',()=>{
 const cell=bodies('.task-duration');
 expect(cell).not.toMatch(/font-family:\s*['"]?Courier/i);
 expect(cell).not.toMatch(/monospace/);
 expect(cell).toMatch(/white-space:\s*nowrap/);
 expect(cell).toMatch(/text-align:\s*right/);
});

test('the Duration header is right-aligned over its values',()=>{
 const head=bodies('.tasks-table th:last-child');
 expect(head).toMatch(/text-align:\s*right/);
 expect(head).toMatch(/white-space:\s*nowrap/);
});
