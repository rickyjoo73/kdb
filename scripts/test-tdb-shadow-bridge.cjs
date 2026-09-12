const fs=require('fs'),vm=require('vm'),assert=require('assert/strict');
const code=fs.readFileSync(require('path').join(__dirname,'import-tdb-shadow.cjs'),'utf8');
function run(args,uid=1014,source=null){let calls=[],reports=[];
 const batch=source===null?{bindings:[{tdb_id:'11111111-1111-4111-8111-111111111111',qid:'Q123'}]}:source;
 const spawnSync=(bin,a,opts)=>{
   assert.equal(bin,'docker');calls.push({args:a,input:opts.input});
   return {status:0,stdout:JSON.stringify(calls.length===1?batch:{dry_run:!a.includes('--apply'),total:1,created:1})};
 };
 const sandbox={
   require:n=>{assert.equal(n,'child_process');return {spawnSync};},
   process:{argv:['node','script',...args],getuid:()=>uid,exit:()=>{throw new Error('EXIT');}},
   console:{log:s=>reports.push(JSON.parse(s))}
 };
 vm.runInNewContext(code,sandbox);return {calls,reports};
}
const dry=run([]);assert.equal(dry.reports[0].dry_run,true);assert.equal(dry.reports[0].next_after,'Q123');assert.equal(dry.calls[1].args.includes('--apply'),false);
const sql=dry.calls[0].input;assert.match(sql,/BEGIN READ ONLY/);assert.match(sql,/statement_timeout='5s'/);assert.match(sql,/LIMIT 25/);
assert.doesNotMatch(sql,/api_key|payload|name_ko|SELECT \*/i);
const apply=run(['--apply','--limit','10','--after','Q100']);assert.equal(apply.calls[1].args.includes('--apply'),true);assert.match(apply.calls[0].input,/LIMIT 10/);
for(const args of [['--limit','101'],['--limit','0'],['--after',"Q1'; DELETE"],['--unknown']]) assert.throws(()=>run(args));
assert.throws(()=>run([],1000),/KDB operating account/);
console.log('PASS TDB bridge: read-only ID allowlist, default dry-run, bounded cursor, explicit apply, KDB UID');
