const {test,expect}=require('bun:test');
const fs=require('fs'),path=require('path');
const {JSDOM}=require('./certificates/support.js');
const {watchUncaught}=require('./watch-uncaught.js');
const t=JSDOM?test:test.skip;
const css=fs.readFileSync(path.join(__dirname,'../../ui/forge-theme.css'),'utf8').replace(/\/\*[\s\S]*?\*\//g,'');

// The manifest sub-tabs read as one card, the way the Operations tab does. The strip is the top band
// and the panel is the body, on the broker screen (#blacksmith) and on the instance screen (#services).
// Both screens must take the same rules, so the gap under the strip is the same on each.
const rules=[...css.matchAll(/([^{}]+)\{([^}]*)\}/g)].map(([,selector,body])=>({selector:selector.trim(),body}));
const SUBTABS=['global','instance-groups','releases','stemcells','features','update','variables','addons','manifest'];
const dom=(screen)=>{
 const d=new JSDOM(`<main><section id="${screen}"><div class="detail-content"><div class="manifest-details-container deployment-manifest-workspace"><bs-tabs><div class="manifest-tabs-nav"></div>${SUBTABS.map(n=>`<div class="manifest-tab-pane" data-tab="${n}"><table class="manifest-table"></table></div>`).join('')}</bs-tabs></div></div></section></main>`);
 watchUncaught(d.window);
 return d.window.document;
};
// An at-rule header is not a selector, so it never matches.
const matches=(el,selector)=>{try{return el.matches(selector);}catch{return false;}};
const matching=(doc,el)=>rules.filter(r=>matches(el,r.selector));
const declared=(list,prop)=>{
 let value;
 for(const r of list){const m=r.body.match(new RegExp('(?:^|[;\\s])'+prop+':\\s*([^;]+)'));if(m)value=m[1].trim();}
 return value;
};

t('the broker and instance manifests take the same rules for the strip, the panels, and the tables',()=>{
 const sets=['blacksmith','services'].map(screen=>{
  const doc=dom(screen);
  const pick=(sel)=>matching(doc,doc.querySelector(sel)).map(r=>r.body);
  return {nav:pick('.manifest-tabs-nav'),pane:pick('.manifest-tab-pane'),table:pick('.manifest-table')};
 });
 expect(sets[1]).toEqual(sets[0]);
 expect(sets[0].pane.length).toBeGreaterThan(0);
});

t('every sub-tab panel has the Operations padding on all sides and the table sits flush in it',()=>{
 for(const screen of ['blacksmith','services']){
  const doc=dom(screen);
  const panes=[...doc.querySelectorAll('.manifest-tab-pane')];
  expect(panes.length).toBe(SUBTABS.length);
  for(const pane of panes){
   const list=matching(doc,pane);
   expect(declared(list,'padding')).toBe('8px');
   expect(declared(list,'border-top')).toBe('0');
   const table=matching(doc,pane.querySelector('.manifest-table'));
   expect(declared(table,'margin')).toBe('0');
   expect(declared(table,'margin-top')).toBeUndefined();
  }
 }
});

t('the strip is the top band of the card and the old offsets are gone',()=>{
 for(const screen of ['blacksmith','services']){
  const doc=dom(screen);
  const nav=matching(doc,doc.querySelector('.manifest-tabs-nav'));
  expect(declared(nav,'border')).toMatch(/^1px solid/);
  expect(declared(nav,'border-bottom-color')).toBeDefined();
 }
 expect(css).not.toMatch(/#blacksmith \.deployment-manifest-workspace/);
 expect(css).not.toMatch(/deployment-manifest-workspace[^{]*\{[^}]*padding:\s*8px 0 0/);
 expect(css).not.toMatch(/deployment-manifest-workspace > \.manifest-tab-content/);
});
