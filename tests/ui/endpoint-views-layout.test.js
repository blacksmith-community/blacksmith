const {test,expect}=require('bun:test');
const fs=require('fs'),path=require('path');
const css=fs.readFileSync(path.join(__dirname,'..','..','ui','blacksmith.css'),'utf8');
const rule=(sel)=>{ const i=css.lastIndexOf(sel+' {'); if(i<0) return ''; return css.slice(i,css.indexOf('}',i)); };

// The marketplace table sits in a panel that already has a frame, so its own
// container adds a second box. The Services selector card does the same.
test('the marketplace table container has no frame of its own',()=>{
 const r=rule('.broker-details-container .marketplace-services-table-container');
 expect(r).toMatch(/border:\s*(none|0)/);
 expect(r).toMatch(/background:\s*transparent|background:\s*none/);
 expect(r).toMatch(/margin-top:\s*0/);
});
test('the org and space selector has no frame of its own',()=>{
 const r=rule('.broker-details-container .org-space-selector');
 expect(r).toMatch(/border:\s*(none|0)/);
 expect(r).toMatch(/background:\s*transparent|background:\s*none/);
 expect(r).toMatch(/padding:\s*0/);
});
test('the gap under the endpoint tabs is 8 to 12px',()=>{
 const r=rule('.broker-details-container bs-tabs > .cf-detail-panel');
 const m=/padding-top:\s*(\d+)px/.exec(r);
 expect(m).not.toBeNull();expect(+m[1]).toBeGreaterThanOrEqual(8);expect(+m[1]).toBeLessThanOrEqual(12);
});
test('the selection table is compact and borderless',()=>{
 const r=rule('.broker-details-container .org-space-table td');
 const m=/padding:\s*(\d+)px\s+(\d+)px/.exec(r);
 expect(m).not.toBeNull();expect(+m[1]).toBeGreaterThanOrEqual(4);expect(+m[1]).toBeLessThanOrEqual(6);
 expect(r).toMatch(/border:\s*(none|0)/);
 expect(rule('.broker-details-container .org-space-table')).toMatch(/border:\s*(none|0)/);
 expect(rule('.broker-details-container .org-space-table td:first-child,\n.broker-details-container .org-space-table td:nth-child(3)')).toMatch(/font-weight:\s*(600|700|bold)/);
});
test('the shared table rules are untouched',()=>{
 expect(css).not.toMatch(/\.broker-details-container\s+(th|thead|table)\b/);
});
