// Operator-only bridge. Run as KDB UID with docker access, never in the app
// service. Reads a bounded ID-only snapshot in a read-only TDB transaction.
// Default: dry-run. No files, credentials, source payloads or labels exported.
const {spawnSync}=require('child_process');
const args=process.argv.slice(2);let apply=false,limit=25,after='';
for(let i=0;i<args.length;i++){
 if(args[i]==='--apply') apply=true;
 else if(args[i]==='--limit') limit=Number(args[++i]);
 else if(args[i]==='--after') after=args[++i];
 else throw new Error('Unknown bridge argument');
}
if(!Number.isInteger(limit)||limit<1||limit>100||typeof after!=='string'||(after!==''&&!/^Q[1-9][0-9]*$/.test(after))) throw new Error('Invalid bounded cursor');
if(typeof process.getuid!=='function'||process.getuid()!==1014) throw new Error('Run as the KDB operating account (UID 1014)');
function run(args,input){const r=spawnSync('docker',args,{input,encoding:'utf8',maxBuffer:128*1024,timeout:30000});if(r.error||r.status!==0)throw new Error('Bounded bridge subprocess failed; no retry and no credential output');return r.stdout;}
// TDB record payload/coordinates may contain derived values despite source_code
// wikidata. None of them are selected. QID is an unapproved identity claim.
const sql=`BEGIN READ ONLY;
SET LOCAL statement_timeout='5s'; SET LOCAL lock_timeout='1s';
WITH bindings AS (
 SELECT l.place_id AS tdb_id,l.external_id AS qid,l.method,l.score,p.operator_locked AS locked,p.place_type AS type
 FROM tdb_place_links l JOIN tdb_places p ON p.id=l.place_id
 WHERE l.source_code='wikidata' AND l.external_id>'${after}' AND l.external_id~'^Q[1-9][0-9]*$'
 AND p.status='active' AND p.merged_into IS NULL
 AND EXISTS(SELECT 1 FROM tdb_src_records r WHERE r.source_code=l.source_code AND r.external_id=l.external_id AND NOT r.deleted)
 ORDER BY l.external_id,l.place_id LIMIT ${limit}
)
SELECT jsonb_build_object('policy','tdb-wikidata-bindings-v1','source',s.code,'license',s.license,'state',s.state,'enabled',s.enabled,'generated',s.generated,'observed_at',now(),'bindings',coalesce((SELECT jsonb_agg(to_jsonb(b) ORDER BY b.qid,b.tdb_id) FROM bindings b),'[]'::jsonb))
FROM tdb_sources s WHERE s.code='wikidata' AND s.license='CC0' AND s.state='live' AND s.enabled AND NOT s.generated;
COMMIT;`;
const raw=run(['exec','-i','tdb-db','sh','-eu','-c','exec psql -X -qAt -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -f -'],sql).trim();
if(!raw) throw new Error('Approved source policy unavailable; no intake');
const batch=JSON.parse(raw);
if(!Array.isArray(batch.bindings)||batch.bindings.length>limit) throw new Error('Invalid source snapshot');
if(batch.bindings.length===0){console.log(JSON.stringify({dry_run:!apply,total:0,after,next_after:null,scope:'bounded ID claims only; not a full migration'}));process.exit(0);}
const report=JSON.parse(run(['exec','-i','kdb-app','/usr/local/bin/kdb-app','tdb-shadow-import',...(apply?['--apply']:[])],JSON.stringify(batch)));
console.log(JSON.stringify({...report,after,next_after:batch.bindings.at(-1).qid,scope:'bounded ID claims only; no master names or TDB records changed'}));
