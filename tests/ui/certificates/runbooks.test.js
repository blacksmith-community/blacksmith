const { test, expect } = require('bun:test');
const H = require('./harness.js');
const { checks, JSDOM } = require('./support.js');

const { ok, failures } = checks();
const t = JSDOM ? test : test.skip;

const h10 = H.instance; const fh = H.fleet;
const sleep=h10.sleep;
t('runbooks', async () => {
  const out={};
  for(const env of ['ws','bad env;rm -rf','']){
    const {w,d,calls,json}=h10.boot(); w.blacksmithData={env};
    w.__routes=[[/certificates\/inventory\?owner/,()=>json(200,{generated_at:'',summary:{},scan:{state:'idle'},capabilities:{},certificates:[]})]];
    const host=d.createElement('div'); d.body.appendChild(host); host.innerHTML=w.__T.renderCertificatesTab(); w.__T.initializeCertificatesTab(); await sleep(120);
    const rbs=[...d.querySelectorAll('details.cert-ov-runbook')];
    out['ov:'+env]=rbs.slice(0,2).map(r=>r.textContent);
    const copies=[...rbs[0].querySelectorAll('.cert-copy-btn')];
    if(env==='ws'){
      ok(copies.length===2,'https runbook has one Copy per command');
      copies[0].click(); await sleep(10); copies[1].click(); await sleep(10);
      ok(w.__copied[0]==='genesis ws rotate-secrets broker/server'&&w.__copied[1]==='genesis ws deploy','each Copy copies only its own line');
      ok(rbs[0].querySelectorAll('pre').length===2,'one pre per command');
      ok(!/Check the exact command/.test(rbs[0].textContent+rbs[1].textContent),'envNote hidden when env known');
      ok(/Add -P to rotate every problematic secret in one pass\./.test(rbs[0].textContent),'overview -P wording');
      ok(!/fixed alone/.test(rbs[0].textContent),'old -P wording gone');
      ok(/not supported in this release/.test(rbs[1].textContent)&&/kit work and lab validation/.test(rbs[1].textContent),'overview rekey wording');
      ok(rbs[1].querySelectorAll('.cert-copy-btn').length===2,'ca runbook has two Copy buttons');
    }
    if(env.startsWith('bad')){
      ok(/genesis <env> rotate/.test(rbs[0].textContent)&&!/rm -rf/.test(rbs[0].textContent),'invalid env falls back to <env>');
      ok(/The broker did not report/.test(rbs[0].textContent),'envNote shown when env unknown');
    }
  }
  // fleet drawer
  const {w,d,server}=await fh.boot();
  await w.blacksmithNav.switchTab('certificates'); await sleep(150);
  const clickRow=async(id)=>{ const tr=d.querySelector(`tr[data-cert-id="${id}"]`); if(!tr) return null; tr.click(); await sleep(150); return d.querySelector('.cert-fleet-drawer'); };
  // make sure all rows show: clear filters
  const clr=[...d.querySelectorAll('button')].find(b=>/Clear filters/.test(b.textContent)); if(clr){clr.click(); await sleep(150);}
  for(const id of ['ca1','unk1']){
    const dr=await clickRow(id);
    if(!dr){ ok(false,'row '+id+' present'); continue; }
    const rb=dr.querySelector('.cert-runbook');
    out['drawer:'+id]=rb?rb.textContent:null;
    ok(!!rb,'drawer runbook for '+id);
    if(rb){ ok(rb.querySelectorAll('.cert-copy-btn').length===2,id+' per-command Copy');
      if(id==='ca1') ok(/not supported in this release/.test(rb.textContent)&&/kit work and lab validation/.test(rb.textContent)&&!/we need a rekey instead/.test(rb.textContent),'drawer CA wording');
      else ok(/Add -P to rotate every problematic secret in one pass\./.test(rb.textContent)&&!/fixed alone/.test(rb.textContent),'drawer -P wording'); }
    ok(d.querySelectorAll('#certificates img,#certificates script,[onerror]').length===0,'no injection '+id);
  }
  expect(failures).toEqual([]);
}, 120000);
