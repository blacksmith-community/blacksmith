const {test,expect}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const data={text:'name: test',parsed:{name:'test',releases:[{name:'release',version:'1'}]}};
test('manifest tabs have linked panels and keyboard selection',()=>{const {w,d}=boot();try{
d.body.innerHTML=w.__T.formatManifestDetails(data,'instance');
const tabs=[...d.querySelectorAll('[role=tab]')];expect(tabs.length).toBe(3);
expect(d.querySelector('[role=tablist]').getAttribute('aria-label')).toBe('Manifest sections');
for(const tab of tabs){const panel=d.getElementById(tab.getAttribute('aria-controls'));expect(panel.getAttribute('role')).toBe('tabpanel');expect(panel.getAttribute('aria-labelledby')).toBe(tab.id);}
tabs[0].focus();tabs[0].dispatchEvent(new w.KeyboardEvent('keydown',{key:'End',bubbles:true}));expect(d.activeElement).toBe(tabs[2]);expect(tabs[2].getAttribute('aria-selected')).toBe('true');expect(tabs[0].tabIndex).toBe(-1);
tabs[2].dispatchEvent(new w.KeyboardEvent('keydown',{key:'ArrowRight',bubbles:true}));expect(d.activeElement).toBe(tabs[0]);
}finally{w.close()}});
test('manifest values stay text and copy handlers preserve quotes safely',()=>{const {w,d}=boot();try{
const bad=`<img src=x onerror="alert(1)">'\\test`;
d.body.innerHTML=w.__T.formatManifestDetails({text:bad,parsed:{name:bad,director_uuid:bad,releases:[{name:bad,url:'javascript:alert(1)',version:2}],features:{[bad]:[bad]},variables:[{name:bad,options:{x:bad}}]}},bad);
expect(d.querySelector('img')).toBeNull();expect(d.querySelector('[onerror]')).toBeNull();expect(d.querySelector('a[href^="javascript:"]')).toBeNull();expect(d.body.textContent).toContain(bad);
let copied;w.copyValue=(event,value)=>copied=value;w.eval('((event)=>{'+d.querySelector('[aria-label="Copy manifest name"]').getAttribute('onclick')+'})({})');expect(copied).toBe(bad);
}finally{w.close()}});

test('icon-only manifest copy completes without a text span',async()=>{const {w,d}=boot();try{
await sleep(40);d.body.innerHTML=w.__T.formatManifestDetails(data,'instance');let copied;Object.defineProperty(w.navigator,'clipboard',{value:{writeText:async text=>{copied=text}},configurable:true});
const button=d.querySelector('.copy-btn-manifest');const id=button.getAttribute('onclick').match(/copyManifest\('([^']+)'/)[1];await w.copyManifest(id,{currentTarget:button});expect(copied).toBe(data.text);expect(button.classList.contains('copied')).toBe(true);
}finally{w.close()}});
