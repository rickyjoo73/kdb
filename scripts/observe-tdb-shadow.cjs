// KDB 운영 계정의 ID-only 관측기. 앱에 Docker socket이나 TDB 비밀값을 주지 않는다.
// 읽기 성공한 완전한 대상 집합만 제출한다. 시간 초과를 원본 삭제로 판단하지 않는다.
const {spawnSync}=require('child_process');
const args=process.argv.slice(2);if(args.length>1||(args.length===1&&args[0]!=='--apply'))throw new Error('Only optional --apply accepted');
const apply=args.includes('--apply');if(process.getuid()!==1014)throw new Error('KDB operating account required');
function run(a,input){const r=spawnSync('docker',a,{input,encoding:'utf8',maxBuffer:128*1024,timeout:35000});if(r.error||r.status!==0)throw new Error('ID-only observation subprocess failed; source result not applied or safely replayable');return JSON.parse(r.stdout.trim());}
const targets=run(['exec','kdb-app','/usr/local/bin/kdb-app','tdb-shadow-observe','--list']);
if(!Array.isArray(targets)||targets.length>100)throw new Error('Invalid target bound');
const uuid=/^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$/;const qid=/^Q[1-9][0-9]*$/;
const expected=new Map();for(const t of targets){if(!uuid.test(t.shadow_id)||!uuid.test(t.tdb_id)||!qid.test(t.qid)||expected.has(t.shadow_id))throw new Error('Invalid exact target set');expected.set(t.shadow_id,t);}
if(!targets.length){console.log(JSON.stringify({dry_run:!apply,total:0,scope:'known source ID observations only'}));process.exit(0);}
const values=targets.map(t=>`('${t.shadow_id}'::uuid,'${t.tdb_id}'::uuid,'${t.qid}'::text)`).join(',');
const types=['tourist_spot','legal_dong','cultural_facility','festival_event','travel_course','leisure_sports','accommodation','shopping','restaurant','transport','heritage','nature','admin_region','road','district','other','food','organization','transit','person','work','education'];
const sql=`BEGIN READ ONLY; SET LOCAL statement_timeout='5s'; SET LOCAL lock_timeout='1s';
WITH requested(shadow_id,tdb_id,qid) AS(VALUES ${values}), records AS(
 SELECT r.shadow_id,r.tdb_id,r.qid,coalesce(l.method,'missing') AS method,coalesce(l.score,0) AS score,coalesce(p.operator_locked,false) AS locked,coalesce(p.place_type::text,'') AS type,
 CASE WHEN s.code IS NULL OR s.license IS DISTINCT FROM 'CC0' OR s.state IS DISTINCT FROM 'live' OR NOT coalesce(s.enabled,false) OR coalesce(s.generated,true) THEN 'source_policy_blocked'
 WHEN p.id IS NULL OR p.status IS DISTINCT FROM 'active' OR p.merged_into IS NOT NULL THEN 'place_inactive'
 WHEN l.place_id IS NULL THEN 'link_missing'
 WHEN NOT EXISTS(SELECT 1 FROM tdb_src_records x WHERE x.source_code='wikidata' AND x.external_id=r.qid AND NOT x.deleted) THEN 'record_deleted'
 WHEN p.place_type::text NOT IN (${types.map(t=>"'"+t+"'").join(',')}) THEN 'type_unsupported' ELSE '' END AS reason
 FROM requested r LEFT JOIN tdb_places p ON p.id=r.tdb_id
 LEFT JOIN tdb_place_links l ON l.place_id=r.tdb_id AND l.source_code='wikidata' AND l.external_id=r.qid
 LEFT JOIN tdb_sources s ON s.code='wikidata'
)
SELECT jsonb_build_object('policy','tdb-id-observation-v1','observed_at',now(),'records',jsonb_agg(jsonb_build_object('shadow_id',r.shadow_id,'available',r.reason='','reason',r.reason,'binding',jsonb_build_object('tdb_id',r.tdb_id,'qid',r.qid,'method',r.method,'score',r.score,'locked',r.locked,'type',r.type)) ORDER BY r.shadow_id)) FROM records r;
COMMIT;`;
const batch=run(['exec','-i','tdb-db','sh','-eu','-c','exec psql -X -qAt -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -f -'],sql);
if(batch.policy!=='tdb-id-observation-v1'||!Array.isArray(batch.records)||batch.records.length!==targets.length)throw new Error('Incomplete source snapshot refused');
for(const r of batch.records){const t=expected.get(r.shadow_id);if(!t||!r.binding||r.binding.tdb_id!==t.tdb_id||r.binding.qid!==t.qid)throw new Error('Changed source target set refused');expected.delete(r.shadow_id);}
if(expected.size)throw new Error('Missing source observations refused');
const report=run(['exec','-i','kdb-app','/usr/local/bin/kdb-app','tdb-shadow-observe',...(apply?['--apply']:[])],JSON.stringify(batch));
console.log(JSON.stringify({...report,scope:'known ID observations only; no identity or language approvals'}));
