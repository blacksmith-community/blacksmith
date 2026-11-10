const {test,expect}=require('bun:test');
const fs=require('fs');
const path=require('path');
const css=fs.readFileSync(path.resolve(__dirname,'..','..','ui','blacksmith.css'),'utf8');
const rule=(selector)=>{const i=css.indexOf(selector+' {');return i<0?null:css.slice(i,css.indexOf('}',i)+1);};

// The Blacksmith palette must lose to wterm's theme classes, so it only
// applies to a terminal that has none. The layout rule keeps the ID scope.
test('the Blacksmith palette applies only when no theme class is present',()=>{
 const sel='#terminal-modal .terminal-instance.wterm:not(.theme-solarized-dark):not(.theme-monokai):not(.theme-light)';
 const palette=css.indexOf(sel);
 expect(palette).toBeGreaterThan(-1);
 const body=css.slice(palette,css.indexOf('}',palette));
 expect(body).toMatch(/--term-bg:\s*#000000/);
 expect(body).toMatch(/--term-color-15:/);
 const layout=rule('#terminal-modal .terminal-instance.wterm');
 expect(layout).not.toBeNull();
 expect(layout).not.toMatch(/--term-bg|--term-color/);
 expect(layout).toMatch(/--term-font-family/);
 expect(layout).toMatch(/padding-block:\s*0/);
});

test('the frame around the grid follows the scheme background',()=>{
 expect(rule('#terminal-modal .terminal-tabset')).toMatch(/background:\s*var\(--terminal-frame-bg,\s*#000\)/);
 expect(css).toMatch(/\.terminal-tab\.active[^{]*\{[^}]*background:\s*var\(--terminal-frame-bg,\s*#000\)/);
});

test('selection stays visible in every scheme',()=>{
 expect(rule('#terminal-modal .wterm ::selection')).toMatch(/background:\s*var\(--term-selection-bg/);
 expect(css).toMatch(/#terminal-modal \.terminal-instance\.wterm\.theme-light\s*\{[^}]*--term-selection-bg:\s*rgba\(64,\s*120,\s*242/);
});

test('the swatch shows a theme without wterm layout and the menu is a popover list',()=>{
 expect(css).toMatch(/#terminal-modal \.terminal-scheme-swatch\s*\{[^}]*all:\s*unset/);
 expect(css).toMatch(/\.terminal-scheme-item\s*\{/);
 expect(css).toMatch(/\.terminal-scheme-item\[aria-checked="true"\]/);
 expect(css).toMatch(/\.terminal-scheme-item:focus-visible/);
});
