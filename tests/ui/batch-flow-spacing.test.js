const {test,expect}=require('bun:test');
const fs=require('fs'),path=require('path');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;
const css=fs.readFileSync(path.resolve(__dirname,'..','..','ui','forge-theme.css'),'utf8');
// Narrow-window overrides live in media blocks, and these checks are about the base rules.
const base=css.replace(/@media[^{]*\{(?:[^{}]*\{[^{}]*\})*[^{}]*\}/g,'');
// Every declaration block whose selector list contains the exact selector, in file order.
const rules=(selector)=>{
 const out=[];const re=/([^{}]+)\{([^{}]*)\}/g;let m;
 while((m=re.exec(base))){ if(m[1].replace(/\/\*[\s\S]*?\*\//g,'').split(',').map(s=>s.trim()).includes(selector)) out.push(m[2]); }
 return out;
};
const declared=(selector,prop)=>{
 const found=rules(selector).map(b=>(b.match(new RegExp(`(?:^|[;\\s])${prop}\\s*:\\s*([^;]+)`))||[])[1]).filter(Boolean);
 return found.length?found[found.length-1].trim():null;
};
async function setupUpgrade(footerHeight){
 const env=boot(),{w,d}=env;
 const observers=[];
 w.ResizeObserver=class{constructor(cb){this.cb=cb;observers.push(this);}observe(el){this.el=el;}disconnect(){}};
 w.Element.prototype.getBoundingClientRect=function(){return {top:0,left:0,right:0,bottom:0,width:100,height:this.classList&&this.classList.contains('batch-flow-footer')?footerHeight.value:0};};
 w.__routes.push(['/b/bosh/stemcells',(u,o,j)=>j(200,[])],['/b/bosh/releases',(u,o,j)=>j(200,[])],['/b/bosh/deployment-releases',(u,o,j)=>j(200,{})],['/b/upgrade/settings',(u,o,j)=>j(200,{})],['/b/upgrade/tasks',(u,o,j)=>j(200,[])]);
 await sleep(30);
 w.serviceInstances={'inst-1':{service_id:'valkey',plan_id:'cluster',name:'Cache 1'}};
 d.getElementById('upgrade').classList.add('active');
 await w.__T.initUpgradeTab();await sleep(30);
 return {...env,observers};
}

t('the footer clearance follows the footer height and not a constant',async()=>{
 const height={value:61};
 const {w,d,observers}=await setupUpgrade(height);try{
 const panel=d.getElementById('upgrade');
 expect(panel.style.getPropertyValue('--batch-footer-height')).toBe('61px');
 height.value=87;
 observers.filter(o=>o.el&&o.el.classList.contains('batch-flow-footer')).forEach(o=>o.cb());
 expect(panel.style.getPropertyValue('--batch-footer-height')).toBe('87px');
 // A hidden panel measures zero, and the last real height stays.
 height.value=0;
 observers.filter(o=>o.el&&o.el.classList.contains('batch-flow-footer')).forEach(o=>o.cb());
 expect(panel.style.getPropertyValue('--batch-footer-height')).toBe('87px');
 }finally{w.close();}
});
t('the scroll padding reads the footer height and no pane adds a fixed clearance',()=>{
 expect(declared('#upgrade','scroll-padding-block-end')).toContain('var(--batch-footer-height');
 expect(css).not.toMatch(/padding-bottom:\s*140px/);
 expect(rules('#upgrade .batch-flow-options > :last-child')).toEqual([]);
 expect(rules('#upgrade .batch-flow-options .command-pane')).toEqual([]);
});
t('the selected badge takes no line until its card is selected',()=>{
 expect(rules('#upgrade .batch-operation-selected').join(';')).not.toContain('visibility');
 expect(declared('#upgrade .rotate-operation-option.is-selected .batch-operation-selected','display')).not.toBe('none');
 expect(declared('#upgrade .batch-operation-selected','display')).toBe('none');
});
t('the operation cards fit the row instead of wrapping a fourth card alone',()=>{
 expect(declared('#upgrade .rotate-operation','grid-template-columns')).toMatch(/^repeat\(auto-fit,\s*minmax\(/);
});
t('the step rhythm uses spacing tokens and no double margins',()=>{
 for(const [sel,prop] of [['#upgrade .batch-flow-options','padding'],['#upgrade .rotate-operation','padding'],['#upgrade .rotate-operation-option','padding'],['#upgrade #batch-version-floor-help','margin'],['#upgrade .batch-additional-releases','padding-top']]){
  expect([sel,declared(sel,prop)]).toEqual([sel,expect.stringContaining('var(--space-')]);
 }
 expect(declared('#upgrade .batch-pause-row + .batch-optional','margin-top')).toBe('0');
});
t('a long step title wraps inside the row and never widens the page',()=>{
 expect(declared('#upgrade #batch-flow-title','max-width')).toBe('100%');
});
t('the versions table is compact, with the optional note inline',()=>{
 expect(declared('#upgrade .batch-upgrade-options-table th:first-child','width')).toBe('8rem');
 expect(css).toMatch(/#upgrade \.batch-upgrade-options :is\(th,td\)[^{]*\{ padding: var\(--space-2\) var\(--space-3\);/);
 expect(declared('#upgrade .batch-upgrade-options .batch-column-note','display')).toBe('inline');
 expect(declared('#upgrade .batch-steps','margin-bottom')).toBe('var(--space-2)');
 expect(declared('#upgrade #batch-version-floor-help','margin')).toBe('var(--space-2) 0 var(--space-3)');
});
