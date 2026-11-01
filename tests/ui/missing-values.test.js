const {test,expect}=require('bun:test');
const fs=require('fs');
const path=require('path');
const H=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const js=fs.readFileSync(path.resolve(__dirname,'..','..','ui','blacksmith.js'),'utf8');

// A value the server did not send shows the one muted em dash, never n/a.
t('the certificate detail table shows the empty value for missing fields',()=>{
 const inst=H.instance.boot();
 try{
  const view=inst.w.__T.CertView;
  const table=view.detail({subject:'CN=db',serialNumber:'0A:0B'});
  const rows=Object.fromEntries([...table.querySelectorAll('tr')].map(tr=>[tr.querySelector('th').textContent,tr.querySelector('td')]));
  for(const label of ['Issuer','Valid From','Valid To','Signature Algorithm']){
   expect(rows[label].textContent.trim()).toBe('—');
   expect(rows[label].querySelector('.empty-value')).not.toBeNull();
   expect(rows[label].querySelector('button')).toBeNull();
  }
  expect(rows.Subject.querySelector('button')).not.toBeNull();
  expect(table.textContent).not.toMatch(/n\/a/i);
 }finally{inst.w.close();}
});

t('no n/a placeholder is left in the interface source',()=>{
 const left=js.split('\n').filter(l=>/'n\/a'|"n\/a"|'N\/A'(?!, *\])|>N\/A</.test(l)&&!l.includes('MANIFEST_PLACEHOLDERS'));
 expect(left).toEqual([]);
});
