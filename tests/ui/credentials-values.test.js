const {test,expect}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');

// The Credentials table hides secrets until asked, collapses certificates,
// keeps arrays whole, and shows the shared empty value for missing values.

const CERT='-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n';
const CHAIN=CERT+CERT;
const KEY='-----BEGIN RSA PRIVATE KEY-----\nQUJD\n-----END RSA PRIVATE KEY-----\n';

async function table(creds){
 const env=boot();await sleep(100);
 const host=env.d.createElement('div');env.d.body.appendChild(host);
 host.innerHTML=env.w.__T.formatCredentials(creds);
 const row=(name)=>[...host.querySelectorAll('tbody tr')].find(r=>r.querySelector('.cred-key').textContent===name);
 return {...env,host,row};
}
const copyOf=async(env,button)=>{let copied;env.w.navigator.clipboard.writeText=async v=>{copied=v;};button.click();await sleep(1);return copied;};

test('the table has no sortable headers',async()=>{
 const t=await table({host:'h'});
 const el=t.host.querySelector('table');
 expect(el.getAttribute('data-sortable')).toBe('false');
 t.w.__T.initializeAllTables();
 expect(t.host.querySelectorAll('th.sortable,.sort-icon').length).toBe(0);
 t.w.close();
});

test('a password is masked until revealed and copies its real value',async()=>{
 const t=await table({host:'h',password:'s3cr3t-test'});
 const cell=t.row('password');
 expect(cell.innerHTML).not.toContain('s3cr3t-test');
 expect(cell.textContent).toContain('••••••••');
 const show=cell.querySelector('button[aria-pressed]');
 expect(show.getAttribute('aria-pressed')).toBe('false');
 expect(show.getAttribute('aria-label')).toBe('Show password');
 expect(show.querySelector('svg.lucide-eye')).not.toBeNull();
 expect(await copyOf(t,cell.querySelector('button[aria-label="Copy password"]'))).toBe('s3cr3t-test');
 expect(cell.innerHTML).not.toContain('s3cr3t-test');
 show.click();
 expect(cell.textContent).toContain('s3cr3t-test');
 expect(show.getAttribute('aria-pressed')).toBe('true');
 expect(show.getAttribute('aria-label')).toBe('Hide password');
 expect(show.querySelector('svg.lucide-eye-off')).not.toBeNull();
 show.click();
 expect(cell.innerHTML).not.toContain('s3cr3t-test');
 expect(cell.textContent).toContain('••••••••');
 t.w.close();
});

test('keys ending in _key, tokens, and secrets are masked but plain keys are not',async()=>{
 const t=await table({api_key:'s3cr3t-a',access_token:'s3cr3t-b',client_secret:'s3cr3t-c',Private_Key:'s3cr3t-d',username:'admin',key:'plain'});
 for(const name of ['api_key','access_token','client_secret','Private_Key']) expect(t.row(name).querySelector('button[aria-pressed]')).not.toBeNull();
 for(const name of ['username','key']) expect(t.row(name).querySelector('button[aria-pressed]')).toBeNull();
 expect(t.host.innerHTML).not.toContain('s3cr3t-');
 t.w.close();
});

test('a private key value is masked whatever its name',async()=>{
 const t=await table({identity:KEY});
 const cell=t.row('identity');
 expect(cell.querySelector('pre')).toBeNull();
 expect(cell.innerHTML).not.toContain('QUJD');
 cell.querySelector('button[aria-pressed]').click();
 expect(cell.querySelector('pre').textContent).toBe(KEY);
 expect(await copyOf(t,cell.querySelector('button[aria-label="Copy identity"]'))).toBe(KEY);
 t.w.close();
});

test('a certificate collapses behind Show PEM and counts its certificates',async()=>{
 const t=await table({ca:CHAIN,leaf:CERT});
 const cell=t.row('ca');
 expect(cell.textContent).toContain('PEM certificate · 2 certificates');
 expect(t.row('leaf').textContent).toContain('PEM certificate · 1 certificate');
 expect(t.row('leaf').textContent).not.toContain('1 certificates');
 const details=cell.querySelector('details');
 expect(details.open).toBe(false);
 expect(details.querySelector('summary').textContent).toBe('Show PEM');
 expect(details.querySelector('pre.credential-multiline').textContent).toBe(CHAIN);
 expect(await copyOf(t,cell.querySelector('button'))).toBe(CHAIN);
 t.w.close();
});

test('an array stays one row with an item per line and copies newline joined',async()=>{
 const t=await table({hosts:['a.example','b.example','c.example'],nested:{list:['x','y']}});
 const cell=t.row('hosts');
 expect(t.host.querySelectorAll('.cred-key')[0].textContent).toBe('hosts');
 const lines=[...cell.querySelectorAll('.credential-array-item')].map(e=>e.textContent);
 expect(lines).toEqual(['a.example','b.example','c.example']);
 expect(await copyOf(t,cell.querySelector('button'))).toBe('a.example\nb.example\nc.example');
 expect(t.row('list')).toBeDefined();
 expect([...t.row('list').querySelectorAll('.credential-array-item')].length).toBe(2);
 t.w.close();
});

test('empty values show the shared empty value with no copy button',async()=>{
 const t=await table({a:'',b:null,c:[],d:false,e:0});
 for(const name of ['a','b','c']){
  expect(t.row(name).querySelector('.empty-value')).not.toBeNull();
  expect(t.row(name).querySelector('button')).toBeNull();
  expect(t.row(name).textContent).not.toContain('empty');
 }
 expect(t.row('d').textContent).toContain('false');
 expect(t.row('e').textContent).toContain('0');
 t.w.close();
});

// Review fixes: connection strings, camelCase names, and array items.
test('a URI with an embedded password is masked and a plain URL is not',async()=>{
 const t=await table({uri:'redis://:s3cr3t-test@10.0.0.5:6379',amqp_uri:'amqps://admin:s3cr3t-test@h:5671/vhost',url:'https://h:8443/path',host_port:'redis://h:6379'});
 for(const name of ['uri','amqp_uri']){
  const cell=t.row(name);
  expect(cell.querySelector('button[aria-pressed]')).not.toBeNull();
  expect(cell.textContent).toContain('••••••••');
 }
 expect(t.host.innerHTML).not.toContain('s3cr3t-test');
 expect(await copyOf(t,t.row('uri').querySelector('button[aria-label="Copy uri"]'))).toBe('redis://:s3cr3t-test@10.0.0.5:6379');
 for(const name of ['url','host_port']) expect(t.row(name).querySelector('button[aria-pressed]')).toBeNull();
 t.w.close();
});

test('camelCase key names are masked like snake_case ones',async()=>{
 const t=await table({apiKey:'s3cr3t-a',privateKey:'s3cr3t-b',clientSecret:'s3cr3t-c',authToken:'s3cr3t-d',passphrase:'s3cr3t-e',displayName:'x'});
 for(const name of ['apiKey','privateKey','clientSecret','authToken','passphrase']) expect(t.row(name).querySelector('button[aria-pressed]')).not.toBeNull();
 expect(t.row('displayName').querySelector('button[aria-pressed]')).toBeNull();
 expect(t.host.innerHTML).not.toContain('s3cr3t-');
 t.w.close();
});

test('array items collapse certificates and mask secrets one by one',async()=>{
 const t=await table({chain:[CERT,CERT],uris:['redis://:s3cr3t-test@h:6379','redis://h:6379']});
 const chain=t.row('chain');
 expect(chain.querySelectorAll('details').length).toBe(2);
 expect(chain.querySelectorAll('.credential-pem-summary')[0].textContent).toBe('PEM certificate · 1 certificate');
 const uris=t.row('uris');
 expect(uris.innerHTML).not.toContain('s3cr3t-test');
 expect(uris.querySelectorAll('button[aria-pressed]').length).toBe(1);
 expect(uris.querySelectorAll('code.credential-array-item')[0].textContent).toBe('redis://h:6379');
 const secretCopy=uris.querySelector('button[aria-label="Copy uris"]');
 expect(await copyOf(t,secretCopy)).toBe('redis://:s3cr3t-test@h:6379');
 t.w.close();
});

test('a secret nested deeper than one level is masked whole',async()=>{
 const t=await table({protocols:{amqp:{uri:'amqp://u:s3cr3t-test@h'}},other:{amqp:{password:'s3cr3t-test'}},peers:[{password:'s3cr3t-test'},{host:'h'}],plain:{a:{b:'ok'}}});
 for(const name of ['protocols','other']){
  expect(t.row(name)).toBeUndefined();
 }
 const cells=[...t.host.querySelectorAll('tbody tr')];
 expect(cells.length).toBe(4);
 expect(t.host.innerHTML).not.toContain('s3cr3t-test');
 expect(t.row('amqp').querySelector('button[aria-pressed]')).not.toBeNull();
 const peers=t.row('peers');
 expect(peers.querySelectorAll('button[aria-pressed]').length).toBe(1);
 expect(peers.querySelector('button[aria-label="Copy peers"]')).not.toBeNull();
 expect(t.row('a').querySelector('button[aria-pressed]')).toBeNull();
 t.w.close();
});
