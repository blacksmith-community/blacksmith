const {test,expect,afterEach}=require('bun:test');
const {main:{boot,idle,TEST_LIMIT}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?(n,f,l)=>test(n,f,l===undefined?TEST_LIMIT:l):test.skip;

// The configuration list shows every certificate the Vault client would
// trust, marks one that is not a CA, and says how many blocks of the bundle
// were skipped.
const envs=[];
afterEach(()=>{
 while(envs.length){ const env=envs.pop(); try{ expect(env.alerts).toEqual([]); } finally { env.w.close(); } }
});

const iso=(ms)=>new Date(ms).toISOString();
const item=(name,common,extra={})=>({name,path:'config:vault.cacert',details:{subject:{commonName:common},issuer:common,notAfter:iso(Date.now()+900*86400000)},...extra});

const screen=async(metadata,certs)=>{
 const env=boot({noDialogs:true,routes:[
  ['/b/internal/certificates/blacksmith',(u,o,j)=>j(200,{success:true,data:{certificates:certs,metadata}})]
 ]});
 envs.push(env);
 await idle(env);
 const host=env.d.createElement('div'); env.d.body.appendChild(host);
 host.innerHTML='<div id="blacksmith-certificates-results"><div id="blacksmith-certificates-list"></div></div>';
 await env.w.fetchBlacksmithCertificates();
 return {...env,list:env.d.getElementById('blacksmith-certificates-list')};
};

t('a certificate that is not a CA is marked and skipped blocks are counted',async()=>{
 const s=await screen({count:2,skippedBlocks:2},[item('Vault CA Certificate','First CA'),item('Vault CA Certificate 2','A Leaf',{notCA:true})]);
 const notes=[...s.list.querySelectorAll('.cert-not-ca')];
 expect(notes.length).toBe(1);
 expect(notes[0].closest('.cert-list-item').textContent).toContain('A Leaf');
 expect(s.list.querySelector('.cert-skipped-blocks').textContent).toBe('2 blocks of this bundle are not trusted, because they are not a certificate that parses.');
});

t('a clean bundle shows neither a mark nor a skipped count',async()=>{
 const s=await screen({count:1},[item('Vault CA Certificate','Only CA')]);
 expect(s.list.querySelector('.cert-not-ca')).toBeNull();
 expect(s.list.querySelector('.cert-skipped-blocks')).toBeNull();
});

t('one skipped block reads in the singular',async()=>{
 const s=await screen({count:1,skippedBlocks:1},[item('Vault CA Certificate','Only CA')]);
 expect(s.list.querySelector('.cert-skipped-blocks').textContent).toBe('1 block of this bundle is not trusted, because it is not a certificate that parses.');
});
