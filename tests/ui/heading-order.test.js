const {test,expect}=require('bun:test');
const H=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;
const {boot,openInstanceTab,sleep,iso,now,day}=H.instance;

// The page title is an h2 and the section toolbar that would hold an h3 is
// hidden, so the first heading in a section must be an h3 and no heading may
// be more than one level below the one before it.
const levels=(root)=>[...root.querySelectorAll('h1,h2,h3,h4,h5,h6')].map((h)=>({level:Number(h.tagName[1]),text:h.textContent.trim()}));
function skips(root,start=2){
 const out=[];let prev=start;
 for(const h of levels(root)){if(h.level>prev+1)out.push(`${h.text} (h${h.level} after h${prev})`);prev=h.level;}
 return out;
}
const cert=(id,over)=>Object.assign({id,key:'sha256:'+id,kind:'leaf',health:'ok',attention:false,coverage:{complete:true},effective_not_after:iso(now+400*day),days_left:400,expiry:'ok',common_name:id,owners:[{kind:'broker'}],observations:[]},over||{});

t('broker Certificates headings skip no level',async()=>{
 const {w,d}=boot();
 w.blacksmithData={env:'ws'};
 w.__routes=[[/certificates\/inventory/,(u,o,json)=>json(200,{generated_at:iso(now),summary:{certificates:1},scan:{state:'idle'},capabilities:{},certificates:[cert('b1')]})]];
 const host=d.createElement('div');d.body.appendChild(host);host.innerHTML=w.__T.renderCertificatesTab();
 w.__T.initializeCertificatesTab();
 await sleep(150);
 expect(host.querySelector('#cert-broker-overview .cert-ov-title').tagName).toBe('H3');
 expect(skips(host)).toEqual([]);
 w.close();
});
t('instance Certificates headings skip no level',async()=>{
 const {w,d}=boot({oat:true});
 w.__routes=[[/certificates\/trusted/,(u,o,json)=>json(200,{success:true,data:{files:[]}})],[/certificates\/instances\//,(u,o,json)=>json(200,{certificates:[cert('i1')],scan:{state:'idle'}})]];
 const host=await openInstanceTab(w,d,'i1');
 await sleep(60);
 expect(skips(host)).toEqual([]);
 w.close();
});

t('instance Operations headings skip no level',async()=>{
 const env=H.main.boot({noDialogs:true});
 const {w,d}=env;
 w.__routes.push(['/credentials',(u,o,json)=>json(200,{host:'h',username:'u',password:'p',vhost:'v',service_type:'rabbitmq'})],['/details',(u,o,json)=>json(200,{service_name:'rabbitmq'})],['/config',(u,o,json)=>json(404,{})],[/\/rabbitmq\/(rabbitmqctl|plugins)\/categories/,(u,o,json)=>json(200,[])]);
 const host=d.createElement('div');host.innerHTML=await w.__T.renderServiceTesting('i1');d.body.appendChild(host);
 expect(levels(host).length).toBeGreaterThan(1);
 expect(skips(host)).toEqual([]);
 w.close();
});
