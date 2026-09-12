// One reviewed scope correction, not bulk rejection reversal or identity approval.
// Real login + CSRF; credentials only on stdin. The preserved UUID is mandatory.
if(typeof fetch!=='function' || process.env.KDB_ADOPT_SOCIETY_SCOPE!=='1'){
 console.error('Node 18+ and explicit KDB_ADOPT_SOCIETY_SCOPE=1 required');process.exit(1);
}
const input=require('readline').createInterface({input:process.stdin,terminal:false});
let received=false;
input.once('close',()=>{if(!received){console.error('No credentials; no change');process.exitCode=1;}});
input.once('line',async line=>{
 received=true;input.close();process.stdin.destroy();
 try{
  const credentials=JSON.parse(line),origin='https://kdb.aiinplanet.com';
  const path='/admin/kentity/7478b5bf-15c7-4fb8-a6b7-6be9eb2d5b8e';
  const health=await fetch(origin+'/v1/health',{signal:AbortSignal.timeout(15000)});
  if(health.status!==200 || (await health.json()).version!=='ownership-20260912-1')throw Error('Expected ownership release is not serving');
  const login=await fetch(origin+'/admin/login',{method:'POST',redirect:'manual',signal:AbortSignal.timeout(15000),body:new URLSearchParams({email:credentials.email,password:credentials.password,next:path})});
  if(login.status!==302)throw Error('Normal login failed; no retry');
  const cookie=(login.headers.get('set-cookie')||'').split(';')[0];if(!cookie)throw Error('No normal session');
  const detail=await fetch(origin+path,{headers:{Cookie:cookie},redirect:'manual',signal:AbortSignal.timeout(15000)});
  const html=await detail.text();
  if(detail.status!==200 || !html.includes('대한적십자사') || /조회 실패|조회하지 못했습니다/.test(html))throw Error('Expected record unavailable');
  if(html.includes('현재 쓰기 책임 공통 Entity')){console.log(JSON.stringify({path,status:'already_adopted_skipped',approved:false}));return;}
  if(!html.includes('일반 사회 복지/구호 기관') || !html.includes('K-엔터테인먼트 엔티티가 아님'))throw Error('Original scope evidence changed; stop for review');
  const formHTML=(html.match(/<form method="POST" action="[^\"]+\/adopt"[\s\S]*?<\/form>/)||[])[0];
  if(!formHTML)throw Error('No eligible operator transition form');
  function field(name){const value=(formHTML.match(new RegExp('name="'+name+'" value="([^"]+)"'))||[])[1];if(!value)throw Error('Missing protected form field '+name);return value;}
  const form=new URLSearchParams({_csrf:field('_csrf'),revision:field('revision'),fingerprint:field('fingerprint'),type:'organization',domains:'society',attested:'yes',source_url:'https://www.redcross.or.kr/main/cm/conts/contsView.do?contsId=1062&mi=2428',reason:'사용자가 요청한 사회 분야 확장에 따른 AI 보조 범위 검토. 기존 기록의 기각 사유가 연예 범위 외 인도주의 구호 기관임을 확인하고 기관 공개 소개를 대조했습니다. 동일 UUID로 미검증 후보 조사만 재개하며 정체성·언어 표기는 승인하지 않습니다.'});
  const response=await fetch(origin+path+'/adopt',{method:'POST',headers:{Cookie:cookie},body:form,redirect:'manual',signal:AbortSignal.timeout(15000)});
  if(response.status!==303 || response.headers.get('location')!==path)throw Error('Transition failed HTTP '+response.status+'; no retry');
  const verify=await fetch(origin+path,{headers:{Cookie:cookie},redirect:'manual',signal:AbortSignal.timeout(15000)});const body=await verify.text();
  if(verify.status!==200 || !body.includes('현재 쓰기 책임 공통 Entity') || !body.includes('신규 Entity 자동 조사') || /조회 실패/.test(body))throw Error('Transition saved but read verification failed; inspect before retry');
  console.log(JSON.stringify({path,status:'same_uuid_candidate_scope_adopted',domain:'society',identity_approved:false,names_approved:false,published:false}));
 }catch(error){console.error(error.message);process.exitCode=1;}
});
