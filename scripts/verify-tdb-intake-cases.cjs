// 한정 운영 점검: 공개 스포츠 인물 1명의 미검증 후보 등록과 발견한 오연결의 재확인.
// 정체성/언어 승인·병합을 호출하지 않는다. 자격 증명은 stdin 한 줄에서만 읽는다.
const readline=require('readline');
const args=process.argv.slice(2);if(args.length>1||(args.length===1&&args[0]!=='--apply'))throw new Error('Only optional --apply is accepted');
if(process.getuid()!==1014)throw new Error('KDB operating account required');
const apply=args.includes('--apply'),origin='https://kdb.aiinplanet.com';
const sports='/admin/kentity/tdb/679aca73-6d7c-4144-afb6-a116060e5b22';
const conflicts=['/admin/kentity/tdb/da1de352-af84-441a-abd6-87bea4ccff3a','/admin/kentity/tdb/b60b1268-0a38-4314-adc5-37f73fc5d11c'];
function form(html,path,action){const marker='action="'+path+'/'+action+'"';const at=html.indexOf(marker);if(at<0)return null;const end=html.indexOf('</form>',at);if(end<0)throw new Error('Incomplete action form');const fragment=html.slice(at,end);const fields=new URLSearchParams();for(const field of ['_csrf','generation','fingerprint']){const match=fragment.match(new RegExp('name="'+field+'" value="([^"]+)"'));if(match)fields.set(field,match[1])};if(!fields.has('_csrf')||!fields.has('generation')||(action==='candidate'&&!fields.has('fingerprint')))throw new Error('Missing protected action fields');return fields;}
const input=readline.createInterface({input:process.stdin,terminal:false});let received=false;input.once('close',()=>{if(!received){console.error('No credential input');process.exitCode=1}});
input.once('line',async line=>{received=true;input.close();process.stdin.destroy();try{
 const credentials=JSON.parse(line);const login=await fetch(origin+'/admin/login',{method:'POST',redirect:'manual',body:new URLSearchParams({email:credentials.email,password:credentials.password,next:sports})});if(login.status!==302)throw new Error('Normal login failed; no retry');const cookie=(login.headers.get('set-cookie')||'').split(';')[0];if(!cookie)throw new Error('No normal login session');
 async function get(path){const r=await fetch(origin+path,{headers:{Cookie:cookie},redirect:'manual'});const html=await r.text();if(r.status!==200||!html.includes('</html>')||html.includes('상세 조회 실패'))throw new Error('Admin detail not ready');return html;}
 // The known commercial-record/station conflict must be blocked before any
 // candidate intake is exercised in this bounded operating check.
 const observations=new Map();for(const path of conflicts){const html=await get(path);if(!html.includes('원본 업종과 독립 출처의 대상 유형이 충돌합니다.')||form(html,path,'candidate'))throw new Error('Known source class conflict not blocked');observations.set(path,html)}
 const sportsHTML=await get(sports);if(!sportsHTML.includes('정유인')||!sportsHTML.includes('Q100623651'))throw new Error('Public sports case changed; stop');const candidate=form(sportsHTML,sports,'candidate');
 console.log(JSON.stringify({dry_run:!apply,source_conflicts_blocked:observations.size,candidate_action_available:!!candidate,recheck_available:conflicts.filter(path=>form(observations.get(path),path,'recheck')).length,identity_approval:false,language_approval:false}));if(!apply)return;
 async function post(path,action,fields){const r=await fetch(origin+path+'/'+action,{method:'POST',headers:{Cookie:cookie},body:fields,redirect:'manual'});if(r.status!==303)throw new Error('Protected '+action+' operation stopped: HTTP '+r.status);console.log(JSON.stringify({path,action,status:r.status}));}
 if(candidate){candidate.set('type','person');candidate.set('domains','sports');candidate.set('reason','사용자 지시에 따른 개발 에이전트의 공개 스포츠 인물 연계 점검입니다. 독립 원천의 미검증 후보만 등록하며 동일인·언어 승인은 하지 않습니다.');await post(sports,'candidate',candidate)}
 for(const path of conflicts){const recheck=form(observations.get(path),path,'recheck');if(recheck){recheck.set('reason','개발 에이전트가 발견한 상업시설과 전철역 외부 ID 충돌을 새 유형 보호 규칙으로 재확인합니다. TDB 원본·언어 표기는 변경하지 않습니다.');await post(path,'recheck',recheck)}}
}catch(error){console.error(error.message);process.exitCode=1}});
