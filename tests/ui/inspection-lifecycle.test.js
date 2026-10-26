const {test,expect}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
test('certificate fetch cannot open a drawer after route cancellation',async()=>{
 const {w,d}=boot();await sleep(50);let release;
 w.__routes.push(['/b/certificates/items/late',()=>new Promise(resolve=>release=()=>resolve({ok:true,json:async()=>({id:'late',name:'late'})}))]);
 const controller=new w.AbortController();const pending=w.blacksmithCerts.openItem('late',{signal:controller.signal});controller.abort();release();
 await expect(pending).resolves.toBe(false);expect(d.querySelector('.cert-fleet-drawer--open')===null).toBe(true);w.close();
});
test('missing certificate inspection reports a recoverable resource error',async()=>{
 const {w,d}=boot();await sleep(50);
 await expect(w.blacksmithCerts.openItem('removed')).rejects.toThrow('This certificate is no longer available.');
 expect(d.querySelector('.cert-fleet-drawer--open')===null).toBe(true);w.close();
});
test('closing a pending certificate inspection invalidates its response',async()=>{
 const {w,d}=boot();await sleep(50);let release;
 w.__routes.push(['/b/certificates/items/late',()=>new Promise(resolve=>release=()=>resolve({ok:true,json:async()=>({id:'late',name:'late'})}))]);
 const pending=w.blacksmithCerts.openItem('late');w.blacksmithCerts.closeDrawer({restore:false});release();
 await expect(pending).resolves.toBe(false);expect(d.querySelector('.cert-fleet-drawer--open')===null).toBe(true);w.close();
});
test('direct broker certificate inspection creates one body drawer and closes to its source',async()=>{
 const {w,d}=boot();await sleep(50);const cert={id:'direct',name:'Direct certificate',findings:[],owners:[]};
 w.__routes.push(['/b/certificates/items/direct',(u,o,json)=>json(200,cert)]);
 const source={screen:'blacksmith',section:'certificates'};let restored;
 w.blacksmithRouter={applying:true,current:{...source,inspection:{kind:'certificate',id:'direct'}},closeInspection(){restored=source;this.current=source;w.blacksmithCerts.closeDrawer({restore:false});}};
 expect(await w.blacksmithCerts.openItem('direct')).toBe(true);
 expect(d.getElementById('cert-fleet-drawer').parentElement===d.body).toBe(true);
 expect(d.getElementById('cert-fleet-drawer').hidden).toBe(false);
 w.blacksmithRouter.applying=false;w.blacksmithCerts.closeDrawer();expect(restored).toEqual(source);expect(d.getElementById('cert-fleet-drawer').hidden).toBe(true);
 w.blacksmithRouter.applying=true;await w.blacksmithCerts.openItem('direct');expect(d.querySelectorAll('#cert-fleet-drawer').length).toBe(1);w.close();
});
