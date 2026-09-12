// Explicit, bounded candidate intake through the real admin login and CSRF form.
// Credentials arrive via stdin, never files/logs. This script does not approve,
// merge, translate or publish any entity. Repeat execution skips existing names.
if (typeof fetch !== 'function' || process.env.KDB_SEED_COMMON_DOMAINS !== '1') {
  console.error('Node 18+ and explicit KDB_SEED_COMMON_DOMAINS=1 required'); process.exit(1);
}
const input=require('readline').createInterface({input:process.stdin,terminal:false});
let received=false;
input.once('close',()=>{if(!received){console.error('No credentials; no intake performed');process.exitCode=1;}});
input.once('line',async line=>{
  received=true;input.close();process.stdin.destroy();
  try {
    const credentials=JSON.parse(line),origin='https://kdb.aiinplanet.com';
    const health=await fetch(origin+'/v1/health',{signal:AbortSignal.timeout(15000)});
    if(health.status!==200 || (await health.json()).version!=='resolver-20260912-1') throw Error('Expected resolver release is not serving; no intake');
    const login=await fetch(origin+'/admin/login',{method:'POST',redirect:'manual',signal:AbortSignal.timeout(15000),body:new URLSearchParams({email:credentials.email,password:credentials.password,next:'/admin/kentity'})});
    if(login.status!==302) throw Error('Normal login failed; no retry');
    const cookie=(login.headers.get('set-cookie')||'').split(';')[0];if(!cookie)throw Error('No normal session');
    for(const item of [{ko:'김대중',type:'person',domain:'politics'},{ko:'정주영',type:'person',domain:'economy'},{ko:'양용은',type:'person',domain:'sports'},{ko:'대한적십자사',type:'organization',domain:'society'}]) {
      const list=await fetch(origin+'/admin/kentity?'+new URLSearchParams({q:item.ko,type:item.type}),{headers:{Cookie:cookie},redirect:'manual',signal:AbortSignal.timeout(15000)});
      const html=await list.text();
      if(list.status!==200 || html.includes('공통 Entity 조회 실패')) throw Error('Candidate lookup unavailable; intake stopped');
      const links=[...html.matchAll(/href="\/admin\/kentity\/([a-f0-9-]{36})"[^>]*>([^<]*)<\/a>/g)];
      if(links.some(m=>m[2]===item.ko)){console.log(JSON.stringify({domain:item.domain,status:'existing_name_skipped',approved:false}));continue;}
      const csrf=(html.match(/name="_csrf" value="([^"]+)"/)||[])[1];if(!csrf)throw Error('Operator candidate form/CSRF unavailable');
      const form=new URLSearchParams({ko:item.ko,type:item.type,domains:item.domain,_csrf:csrf,request_key:'domain-baseline-20260912-'+item.domain,reason:'분야 확장 초도 후보. 이름만으로 대상 확정하지 않고 외부 자동 조사와 동일인·표기 검수를 기다립니다.'});
      const response=await fetch(origin+'/admin/kentity/candidates',{method:'POST',headers:{Cookie:cookie},body:form,redirect:'manual',signal:AbortSignal.timeout(15000)});
      const path=response.headers.get('location')||'';
      if(response.status!==303 || !/^\/admin\/kentity\/[a-f0-9-]{36}$/.test(path))throw Error('Candidate intake failed: HTTP '+response.status);
      const detail=await fetch(origin+path,{headers:{Cookie:cookie},redirect:'manual',signal:AbortSignal.timeout(15000)});
      const body=await detail.text();
      if(detail.status!==200 || !body.includes('신규 Entity 자동 조사') || /원장 조회 실패|공통 Entity 조회 실패/.test(body))throw Error('Candidate was created but research UI verification failed; inspect before further intake');
      console.log(JSON.stringify({domain:item.domain,status:'candidate_created',path,approved:false,published:false}));
    }
  }catch(error){console.error(error.message);process.exitCode=1;}
});
