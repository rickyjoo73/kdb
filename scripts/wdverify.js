// wdverify.js — TDB 자동 연결(kowiki+en-fp)을 위키데이터 항목 원문과 대조한다.
// 입력  /work/cohort.csv   entity_id,canonical_ko,qid
// 출력  /work/verdicts.csv entity_id,qid,verdict,ko_label,en_label,p31
//       /work/labels.csv   entity_id,locale,label      (항목의 현재 라벨 — SQL 이 표기와 대조)
const fs=require('fs');
const UA='kdb-link-audit/1.0 (https://kdb.aiinplanet.com) node/20';
const LOCALES={ko:'ko',en:'en',ja:'ja',es:'es',fr:'fr',de:'de',id:'id',vi:'vi','zh-hans':'zh-Hans','zh-hant':'zh-Hant',zh:'zh'};
const norm=s=>(s||'').toLowerCase().replace(/\s+/g,'');
const sleep=ms=>new Promise(r=>setTimeout(r,ms));
const csv=s=>'"'+String(s==null?'':s).replace(/"/g,'""')+'"';
// CSV 한 줄을 필드로 나눈다. psql COPY 는 **필요할 때만** 인용하므로 둘 다 받아야 한다.
// (처음엔 전부 인용된 것으로 가정했다가 한 건도 못 읽고 "조회 0건"을 성공으로 끝냈다.)
function splitCSV(line){
  const out=[];let cur='',q=false;
  for(let i=0;i<line.length;i++){
    const c=line[i];
    if(q){ if(c==='"'){ if(line[i+1]==='"'){cur+='"';i++} else q=false } else cur+=c }
    else if(c==='"') q=true;
    else if(c===','){ out.push(cur); cur='' }
    else cur+=c;
  }
  out.push(cur);return out;
}
const raw=fs.readFileSync('/work/cohort.csv','utf8').trim();
const rows=(raw===''?[]:raw.split('\n')).map(l=>{
  const f=splitCSV(l);
  if(f.length<3||!f[0]||!f[2]) return null;
  return {id:f[0],ko:f[1],qid:f[2]};
}).filter(Boolean);
if(rows.length===0){
  console.error('cohort.csv 에서 읽은 행이 0이다. 빈 조회를 성공으로 끝내지 않는다.');
  process.exit(2);
}
(async()=>{
  const V=[],L=[];let n=0,ok=0;
  for(const r of rows){
    n++;
    let body=null,got=false,err=null;
    for(let a=0;a<3&&!got;a++){
      try{
        const res=await fetch('https://www.wikidata.org/wiki/Special:EntityData/'+r.qid+'.json',
          {headers:{'User-Agent':UA,'Accept':'application/json'}});
        if(res.status===429||res.status>=500){await sleep(1500*(a+1));continue}
        if(!res.ok){err='HTTP_'+res.status;break}
        body=await res.json();got=true;
      }catch(e){err='NET';await sleep(800*(a+1))}
    }
    if(!got){V.push([r.id,r.qid,'FETCH_FAIL',err||'','','']);await sleep(90);continue}
    const ent=body.entities&&body.entities[r.qid];
    if(!ent||ent.missing!==undefined){V.push([r.id,r.qid,'NO_ITEM','','','']);await sleep(90);continue}
    // 재지정(redirect)된 항목은 받지 않는다 — 우리가 들고 있는 QID 가 그 대상이 아니다.
    if(ent.id&&ent.id!==r.qid){V.push([r.id,r.qid,'REDIRECTED',ent.id,'','']);await sleep(90);continue}
    const lab=ent.labels||{},ali=ent.aliases||{};
    const koL=lab.ko?lab.ko.value:'';
    const koA=(ali.ko||[]).map(a=>a.value);
    const enL=lab.en?lab.en.value:'';
    const p31=((ent.claims&&ent.claims.P31)||[]).map(c=>{try{return c.mainsnak.datavalue.value.id}catch(e){return null}}).filter(Boolean);
    const koMatch=(koL&&norm(koL)===norm(r.ko))||koA.some(a=>norm(a)===norm(r.ko));
    const isHuman=p31.indexOf('Q5')>=0;
    let verdict='OK';
    if(!isHuman) verdict='NOT_HUMAN';
    else if(!koMatch) verdict='KO_MISMATCH';
    V.push([r.id,r.qid,verdict,koL,enL,p31.slice(0,4).join(' ')]);
    if(verdict==='OK'){
      ok++;
      for(const k of Object.keys(lab)){
        const kk=LOCALES[k.toLowerCase()];
        if(kk) L.push([r.id,kk,lab[k].value]);
      }
    }
    if(n%100===0)console.error('  ...'+n+'/'+rows.length+'  OK='+ok);
    await sleep(90);
  }
  fs.writeFileSync('/work/verdicts.csv',V.map(a=>a.map(csv).join(',')).join('\n')+'\n');
  fs.writeFileSync('/work/labels.csv',L.map(a=>a.map(csv).join(',')).join('\n')+'\n');
  const cnt={};V.forEach(a=>cnt[a[2]]=(cnt[a[2]]||0)+1);
  console.log('조회 '+rows.length+'건 — '+Object.entries(cnt).map(([k,v])=>k+' '+v).join(' · '));
  console.log('라벨 행 '+L.length);
})();
