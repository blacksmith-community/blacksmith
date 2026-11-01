const {test,expect}=require('bun:test');
const fs=require('fs'),path=require('path');
const css=fs.readFileSync(path.join(__dirname,'../../ui/forge-theme.css'),'utf8');
const routes=fs.readFileSync(path.join(__dirname,'../../ui/routes.js'),'utf8');

// The router moves focus to the active panel after a destination loads so
// screen readers start at the content. The panel is not operable, so the
// generic [tabindex] focus ring would draw a rule across its top and bottom.
test('the router focus target is the panel the focus ring rule would otherwise outline',()=>{
 expect(routes).toContain(".tab-panel.active .detail-content");
 expect(routes).toContain("setAttribute('tabindex','-1')");
});

test('a programmatic focus target draws no focus ring, in normal and high contrast',()=>{
 const rule=(selector)=>{
  const at=css.indexOf(selector);
  expect(at).toBeGreaterThan(-1);
  return css.slice(at,css.indexOf('}',at));
 };
 expect(rule('.detail-content[tabindex="-1"]:focus-visible')).toMatch(/outline:\s*none/);
 expect(rule(':root[data-contrast="high"] .detail-content[tabindex="-1"]:focus-visible')).toMatch(/outline:\s*none/);
 expect(rule('.tab-panel[tabindex="-1"]:focus-visible')).toMatch(/outline:\s*none/);
});
