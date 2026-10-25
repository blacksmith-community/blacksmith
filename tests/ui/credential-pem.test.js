const {test,expect}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
test('flat and nested credentials preserve PEM lines and exact clipboard values',async()=>{
 const {w,d}=boot();await sleep(100);
 const pem='-----BEGIN CERTIFICATE-----\nMIIB\nABCDEF\n-----END CERTIFICATE-----\n';
 const host=d.createElement('div');d.body.appendChild(host);
 for(const credentials of [{ca_certificate:pem},{credentials:{ca_certificate:pem}}]){
  host.innerHTML=w.__T.formatCredentials(credentials);
  expect(host.querySelector('pre')?.textContent).toBe(pem);
  let copied;w.navigator.clipboard.writeText=async value=>{copied=value;};host.querySelector('button').click();await sleep(1);expect(copied).toBe(pem);
 }
 host.innerHTML=w.__T.formatCredentials({password:'<img src=x>"\'&',enabled:false,count:0});
 expect(host.querySelector('img')).toBe(null);expect(host.textContent).toContain('false');expect(host.textContent).toContain('0');
 w.close();
});
