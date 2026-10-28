// A booted Batch Operations tab with a scripted certificate inventory, for
// the suites that drive the rotation flow's steps.
const {main:{boot,sleep}}=require('./certificates/harness.js');

const DAY=86400000;
const iso=(days)=>new Date(Date.now()+days*DAY).toISOString();
const cert=(key,variable,days,id,name,over={})=>({key,variable,kind:'leaf',days_left:days,effective_not_after:iso(days),expiry:iso(days),owners:[{kind:'instance',instance_id:id,instance_name:name}],rotation:{allowed:true,modes:['redeploy','patch'],reasons:[]},findings:[],...over});

async function setupRotate({certs=[],caps={rotation:{enabled:true},patch_in_place:{enabled:true}},instances,start=true,bootOptions={}}={}){
  const env=boot({oat:true,...bootOptions}),{w,d}=env;
  let inventory={certificates:certs,capabilities:caps,settings:{warning_days:30,critical_days:7}};
  w.__routes.push(['/b/bosh/stemcells',(u,o,j)=>j(200,[])],['/b/bosh/releases',(u,o,j)=>j(200,[])],['/b/bosh/deployment-releases',(u,o,j)=>j(200,{})],['/b/upgrade/settings',(u,o,j)=>j(200,{})],
    ['/b/certificates/inventory',(u,o,j)=>j(200,inventory)],[/\/b\/upgrade\/tasks$/,(u,o,j)=>j(200,[])]);
  w.plansData={services:[{id:'svc1',name:'valkey'}]};
  w.serviceInstances=instances||Object.fromEntries([...new Set(certs.flatMap(c=>c.owners.map(o=>o.instance_id)))].map(id=>[id,{service_id:'svc1',plan_id:'p1',plan:{id:'p1',name:'small'}}]));
  await sleep(40);
  d.getElementById('upgrade').classList.add('active');
  await w.__T.initUpgradeTab();
  if(start){ await w.__T.requestOperation('rotate'); await sleep(80); }
  return {...env,T:w.__T,sleep,setInventory:(next)=>{inventory={...inventory,...next};}};
}
module.exports={setupRotate,cert,iso,sleep};
