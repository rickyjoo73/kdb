// wdverify-batch.js — wdverify.js 와 **같은 판정**을 하되, 한 번에 50개씩 묻는다.
//
// 왜 일괄인가. 운영자가 "qid를 너무 조회하면 차단되지 않을까?" 라고 물었다. 맞는 걱정이다.
// Special:EntityData 는 QID 하나에 요청 하나라 17,237건이면 요청 17,237회다.
// wbgetentities 는 **ids 에 50개**를 받는다 → 345회. 실측 1회 2.0초·622KB.
// 같은 일을 요청 50분의 1로 한다. 조회량을 줄이는 것이 예의고, 그게 차단을 안 만든다.
//
// 입력  /work/cohort.csv   entity_id,canonical_ko,qid,entity_type
// 출력  /work/verdicts.csv entity_id,qid,verdict,ko_label,en_label,p31,entity_type
//       /work/labels.csv   entity_id,locale,label     — 항목의 현재 라벨
//       /work/claims.csv   entity_id,prop,value       — P106(직업)·P569(생년). 분야 판정용
//       10묶음(500건)마다 .partial.csv — 중간에 죽어도 잃지 않는다
const fs=require('fs');
const UA='kdb-link-audit/1.1 (https://kdb.aiinplanet.com) node/20';
const LOCALES={ko:'ko',en:'en',ja:'ja',es:'es',fr:'fr',de:'de',id:'id',vi:'vi','zh-hans':'zh-Hans','zh-hant':'zh-Hant',zh:'zh'};
const LANGS=Object.keys(LOCALES).join('|');
const norm=s=>(s||'').toLowerCase().replace(/\s+/g,'');
// ★wdverify.js 와 **같은 목록**이어야 한다. internal/kdb/wikidata/client.go 의
//   nameElementClasses 가 원본이다. 세 곳이 갈라지면 같은 항목을 서로 다르게 판정한다.
const NAME_ELEMENT=new Set(['Q202444','Q12308941','Q11879590','Q3409032','Q101352','Q1243157',
 'Q4167410','Q13406463','Q4167836','Q17442446','Q15184295','Q11266439','Q66087861','Q22808320','Q106589819']);
const BATCH=50;                 // wbgetentities 의 상한
const PAUSE=250;                // 묶음 사이. 초당 4묶음=200 QID 를 넘지 않는다
const sleep=ms=>new Promise(r=>setTimeout(r,ms));
const csv=s=>'"'+String(s==null?'':s).replace(/"/g,'""')+'"';
// psql COPY 는 **필요할 때만** 인용한다. 둘 다 받아야 한다.
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
  if(f.length<4||!f[0]||!f[2]||!/^Q\d+$/.test(f[2])) return null;
  return {id:f[0],ko:f[1],qid:f[2],type:f[3]};
}).filter(Boolean);
if(rows.length===0){
  console.error('cohort.csv 에서 읽은 행이 0이다. 빈 조회를 성공으로 끝내지 않는다.');
  process.exit(2);
}
const claimIds=(ent,p)=>((ent.claims&&ent.claims[p])||[])
  .map(c=>{try{return c.mainsnak.datavalue.value.id}catch(e){return null}}).filter(Boolean);

(async()=>{
  const V=[],L=[],C=[];let done=0,ok=0,batches=0;
  const flush=(sfx)=>{
    fs.writeFileSync('/work/verdicts'+sfx+'.csv',V.map(a=>a.map(csv).join(',')).join('\n')+(V.length?'\n':''));
    fs.writeFileSync('/work/labels'+sfx+'.csv',  L.map(a=>a.map(csv).join(',')).join('\n')+(L.length?'\n':''));
    fs.writeFileSync('/work/claims'+sfx+'.csv',  C.map(a=>a.map(csv).join(',')).join('\n')+(C.length?'\n':''));
  };
  for(let i=0;i<rows.length;i+=BATCH){
    const chunk=rows.slice(i,i+BATCH);
    // 같은 QID 를 두 대상이 들고 있을 수 있다(I01 위반 후보). ids 는 중복을 못 받으므로
    // 유일하게 만들어 묻고, 응답은 대상별로 다시 나눠 준다.
    const uniq=[...new Set(chunk.map(r=>r.qid))];
    let ents=null,err=null;
    for(let a=0;a<3&&!ents;a++){
      try{
        const u='https://www.wikidata.org/w/api.php?action=wbgetentities&format=json'
          +'&props=labels%7Caliases%7Cclaims&languages='+encodeURIComponent(LANGS)
          +'&ids='+encodeURIComponent(uniq.join('|'));
        const res=await fetch(u,{headers:{'User-Agent':UA,'Accept':'application/json'}});
        if(res.status===429||res.status>=500){await sleep(2000*(a+1));continue}
        if(!res.ok){err='HTTP_'+res.status;break}
        const j=await res.json();
        if(j.error){err='API_'+(j.error.code||'?');break}
        ents=j.entities||{};
      }catch(e){err='NET';await sleep(1000*(a+1))}
    }
    batches++;
    for(const r of chunk){
      done++;
      if(!ents){V.push([r.id,r.qid,'FETCH_FAIL',err||'','','',r.type]);continue}
      const ent=ents[r.qid];
      if(!ent||ent.missing!==undefined){V.push([r.id,r.qid,'NO_ITEM','','','',r.type]);continue}
      // 재지정된 항목 — 우리가 들고 있는 QID 가 그 대상이 아니다.
      if(ent.id&&ent.id!==r.qid){V.push([r.id,r.qid,'REDIRECTED',ent.id,'','',r.type]);continue}
      const lab=ent.labels||{},ali=ent.aliases||{};
      const koL=lab.ko?lab.ko.value:'';
      const koA=(ali.ko||[]).map(a=>a.value);
      const enL=lab.en?lab.en.value:'';
      const p31=claimIds(ent,'P31');
      const koMatch=(koL&&norm(koL)===norm(r.ko))||koA.some(a=>norm(a)===norm(r.ko));
      const isHuman=p31.indexOf('Q5')>=0;
      const isNameEl=p31.some(q=>NAME_ELEMENT.has(q));
      // 유형을 보고 판정한다. 장소의 항목이 사람이 아닌 것은 당연하다.
      let verdict='OK';
      if(isNameEl) verdict='NAME_ELEMENT';
      else if(r.type==='person' && !isHuman) verdict='TYPE_MISMATCH_NOT_HUMAN';
      else if(r.type!=='person' && isHuman)  verdict='TYPE_MISMATCH_IS_HUMAN';
      else if(!koMatch) verdict='KO_MISMATCH';
      V.push([r.id,r.qid,verdict,koL,enL,p31.slice(0,4).join(' '),r.type]);
      if(verdict!=='OK') continue;
      ok++;
      for(const k of Object.keys(lab)){
        const kk=LOCALES[k.toLowerCase()];
        if(kk) L.push([r.id,kk,lab[k].value]);
      }
      // 분야 판정 재료. **값을 해석하지 않고 그대로 적는다** — 직업 QID 를 우리 분야로
      // 옮기는 것은 별개의 결정이고, 지어내지 않으려면 원값이 남아 있어야 한다(D-37).
      for(const q of claimIds(ent,'P106')) C.push([r.id,'P106',q]);
      for(const c of ((ent.claims&&ent.claims.P569)||[])){
        try{ C.push([r.id,'P569',c.mainsnak.datavalue.value.time]) }catch(e){}
      }
    }
    if(batches%10===0){
      console.error('  ...'+done+'/'+rows.length+'  묶음 '+batches+'  OK='+ok);
      flush('.partial');
    }
    await sleep(PAUSE);
  }
  flush('');
  for(const f of ['verdicts.partial','labels.partial','claims.partial'])
    { try{fs.unlinkSync('/work/'+f+'.csv')}catch(e){} }
  const cnt={};V.forEach(a=>cnt[a[2]]=(cnt[a[2]]||0)+1);
  console.log('조회 '+rows.length+'건 / 요청 '+batches+'회 — '
    +Object.entries(cnt).sort((a,b)=>b[1]-a[1]).map(([k,v])=>k+' '+v).join(' · '));
  console.log('라벨 행 '+L.length+' · 주장 행 '+C.length);
  const byType={};
  V.forEach(a=>{const t=a[6]||'?';byType[t]=byType[t]||{};byType[t][a[2]]=(byType[t][a[2]]||0)+1});
  console.log('\n-- 유형별 --');
  Object.keys(byType).sort().forEach(t=>{
    const m=byType[t],tot=Object.values(m).reduce((x,y)=>x+y,0);
    console.log('  '+t.padEnd(12)+' 계 '+String(tot).padStart(6)+'  '+
      Object.entries(m).sort((a,b)=>b[1]-a[1]).map(([k,v])=>k+' '+v).join(' · '));
  });
  if(ok===0){ console.error('OK 가 0건이다. 빈 결과를 성공으로 끝내지 않는다.'); process.exit(2); }
})();
