const {test, expect} = require('bun:test');
const {main:{boot,sleep}} = require('./certificates/harness.js');

async function fixture() {
 const env=boot(); const {w,d}=env; await sleep(50);
 d.getElementById('blacksmith').innerHTML=w.__T.renderBrokerTab();
 const manager=w.CFEndpointManager;
 manager.endpoints={A:{name:'A',endpoint:'https://a.example'},B:{name:'B',endpoint:'https://b.example'}};
 manager.renderEndpointsList();
 return {...env,manager};
}
test('selecting and restoring endpoints never connects automatically',async()=>{
 const {w,manager,calls}=await fixture();
 manager.selectEndpoint('A'); manager.selectEndpoint('B');
 await sleep(220);
 expect(calls.filter(c=>c.includes('/connect'))).toEqual([]);
 expect(manager.selectedEndpoint).toBe('B');
 expect(manager.selectEndpoint('missing')).toBe(false);
 expect(manager.selectedEndpoint).toBe('B');
 w.close();
});
test('a connection response belongs to the endpoint explicitly connected',async()=>{
 const {w,manager}=await fixture(); let release;
 w.__routes.push(['/A/connect',()=>new Promise(r=>release=()=>r({ok:true,json:async()=>({success:true})}))]);
 manager.selectEndpoint('A'); const request=manager.toggleConnection();
 manager.selectEndpoint('B'); release(); await request;
 expect(manager.connectionStates.A.connected).toBe(true);
 expect(manager.connectionStates.B?.connected || false).toBe(false);
 expect(w.document.getElementById('connection-status-text').textContent).toBe('Not connected');
 w.close();
});
test('late marketplace response cannot replace a newly selected endpoint',async()=>{
 const {w,d,manager}=await fixture(); let release;
 manager.connectionStates={A:{connected:true},B:{connected:true}};
 w.__routes.push(['/A/marketplace',()=>new Promise(r=>release=()=>r({ok:true,json:async()=>({services:[{name:'OLD A'}]})}))],
 ['/B/marketplace',(u,o,json)=>json(200,{services:[{name:'CURRENT B'}]})]);
 manager.selectEndpoint('A'); manager.selectEndpoint('B'); await sleep(20);
 release(); await sleep(20);
 expect(d.querySelector('[data-panel=marketplace]').textContent).toContain('CURRENT B');
 expect(d.querySelector('[data-panel=marketplace]').textContent).not.toContain('OLD A');
 w.close();
});
test('endpoint draft survives close and failed save without storage persistence',async()=>{
 const {w,d}=await fixture();
 w.showCFEndpointForm();
 const name=d.getElementById('cf-endpoint-name'); name.value='draft'; name.dispatchEvent(new w.Event('input',{bubbles:true}));
 const pw=d.getElementById('cf-endpoint-password'); pw.value='secret'; pw.dispatchEvent(new w.Event('input',{bubbles:true}));
 w.hideCFEndpointForm(); await sleep(320); w.showCFEndpointForm();
 expect(d.getElementById('cf-endpoint-name').value).toBe('draft');
 expect(d.getElementById('cf-endpoint-password').value).toBe('secret');
 expect(w.localStorage.getItem('cf-endpoint-data') || '').not.toContain('secret');
 d.getElementById('cf-endpoint-url').value='https://cf.example';
 d.getElementById('cf-endpoint-username').value='operator';
 await w.saveCFEndpoint();
 expect(d.querySelector('#cf-endpoint-form [role=alert]').textContent).toContain('Failed to save');
 expect(d.getElementById('cf-endpoint-password').value).toBe('secret');
 w.close();
});

test('saved endpoint preference restores without trusting old connection status',async()=>{
 const {w,manager,calls}=await fixture();
 w.localStorage.setItem('cf-endpoint-data',JSON.stringify({selectedEndpoint:'B',connectionStates:{B:{connected:true}}}));
 w.__routes.push(['/b/cf/endpoints',(u,o,json)=>json(200,{endpoints:manager.endpoints})]);
 await manager.loadEndpoints(); await sleep(180);
 expect(manager.selectedEndpoint).toBe('B');
 expect(manager.connectionStates.B?.connected || false).toBe(false);
 expect(calls.filter(c=>c.includes('/connect'))).toEqual([]);
 w.close();
});
test('old organization spaces cannot replace the current organization',async()=>{
 const {w,d,manager}=await fixture(); let release;
 manager.selectEndpoint('A');
 d.querySelector('[data-panel=services]').innerHTML='<select id="org-selector"><option value="one">One</option><option value="two">Two</option></select><select id="space-selector"></select><div id="services-results"></div>';
 w.__routes.push(['/orgs/one/spaces',()=>new Promise(r=>release=()=>r({ok:true,json:async()=>({spaces:[{guid:'old',name:'Old space'},{guid:'old2',name:'Old 2'}]})}))],['/orgs/two/spaces',(u,o,j)=>j(200,{spaces:[{guid:'new',name:'New space'},{guid:'new2',name:'New 2'}]})]);
 const old=manager.loadSpaces('A','one');
 d.getElementById('org-selector').value='two'; await manager.loadSpaces('A','two');
 release(); await old;
 expect(d.getElementById('space-selector').textContent).toContain('New space');
 expect(d.getElementById('space-selector').textContent).not.toContain('Old space');
 w.close();
});

test('endpoint and marketplace labels are rendered as text',async()=>{
 const {w,d,manager}=await fixture();
 const name='quote\'"><img src=x onerror=alert(1)>';
 manager.endpoints[name]={name,endpoint:'https://cf.example'};
 manager.renderEndpointsList();
 expect(d.querySelectorAll('#cf-endpoint-select img').length).toBe(0);
 expect([...d.getElementById('cf-endpoint-select').options].some(o=>o.value===name)).toBe(true);
 manager.selectEndpoint(name);
 w.__routes.push(['/marketplace',(u,o,j)=>j(200,{services:[{name:'<img src=x>',description:'<script>bad</script>'}]})]);
 await manager.loadMarketplaceTab(d.querySelector('[data-panel=marketplace]'),name);
 expect(d.querySelectorAll('[data-panel=marketplace] img,[data-panel=marketplace] script').length).toBe(0);
 expect(d.querySelector('[data-panel=marketplace]').textContent).toContain('<img src=x>');
 w.close();
});
test('repeated Connect activation does not submit duplicate connection tests',async()=>{
 const {w,manager,calls}=await fixture(); let release;
 w.__routes.push(['/A/connect',()=>new Promise(r=>release=()=>r({ok:true,json:async()=>({success:true})}))]);
 manager.selectEndpoint('A'); const first=manager.toggleConnection();
 await manager.toggleConnection();
 expect(calls.filter(c=>c.includes('/A/connect')).length).toBe(1);
 release(); await first;
 w.close();
});

test('broker initialization resolves only after endpoint choices are ready',async()=>{
 const {w,manager}=await fixture();let release;let ready=false;
 manager.loadEndpoints=()=>new Promise(resolve=>release=()=>{ready=true;resolve();});
 const pending=w.__T.initBrokerTab();expect(typeof pending?.then).toBe('function');expect(ready).toBe(false);
 release();await pending;expect(ready).toBe(true);w.close();
});
