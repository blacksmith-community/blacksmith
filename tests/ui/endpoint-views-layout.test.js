const {test,expect}=require('bun:test');
const fs=require('fs'),path=require('path');
const css=fs.readFileSync(path.join(__dirname,'..','..','ui','blacksmith.css'),'utf8');
const rule=(sel)=>{ const i=css.lastIndexOf('\n'+sel+' {')+1; if(i<=0) return ''; return css.slice(i,css.indexOf('}',i)); };

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

// Round four. The tab panel's own frame goes, so the tabs read as a plain strip with an underline marker.
test('the endpoint tabs are a plain strip with no frame around the panel',()=>{
 const nav=rule('.cf-endpoint-tabs > .broker-tabs-nav');
 expect(nav).toMatch(/background:\s*transparent/);
 expect(nav).toMatch(/border-radius:\s*0/);
 expect(nav).toMatch(/border-bottom:\s*1px solid/);
 const pane=rule('.cf-endpoint-tabs > .broker-tab-pane');
 expect(pane).toMatch(/border:\s*(none|0)/);
 expect(pane).toMatch(/border-radius:\s*0/);
 expect(pane).toMatch(/background:\s*transparent/);
 expect(pane).toMatch(/padding-inline:\s*0|padding-left:\s*0/);
});
test('the active endpoint tab has an underline marker',()=>{
 const active=rule('.cf-endpoint-tabs .broker-tab-btn.active');
 expect(active).toMatch(/border-bottom-color:\s*var\(--/);
 expect(active).toMatch(/font-weight:\s*(600|700)/);
 expect(active).toMatch(/box-shadow:\s*none/);
 expect(rule('.cf-endpoint-tabs .broker-tab-btn')).toMatch(/border-bottom:\s*2px solid transparent/);
});
test('the endpoint view marks its tab set',()=>{
 const js=fs.readFileSync(path.join(__dirname,'..','..','ui','blacksmith.js'),'utf8');
 expect(js).toMatch(/<bs-tabs class="cf-endpoint-tabs">\s*<div class="broker-tabs-nav"/);
});

test('the selection table has no fill of its own, so dark mode shows no card',()=>{
 expect(rule('[data-theme="dark"] .broker-details-container .org-space-table td')).toMatch(/background:\s*transparent/);
 expect(rule('.broker-details-container .org-space-table')).toMatch(/background:\s*transparent/);
});
