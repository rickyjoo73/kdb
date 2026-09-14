// wdverify.js — TDB 자동 연결(kowiki+en-fp)을 위키데이터 항목 원문과 대조한다.
// 입력  /work/cohort.csv   entity_id,canonical_ko,qid,entity_type
// 출력  /work/verdicts.csv entity_id,qid,verdict,ko_label,en_label,p31,entity_type
//       /work/labels.csv   entity_id,locale,label      (항목의 현재 라벨 — SQL 이 표기와 대조)
const fs=require('fs');
const UA='kdb-link-audit/1.0 (https://kdb.aiinplanet.com) node/20';
const LOCALES={ko:'ko',en:'en',ja:'ja',es:'es',fr:'fr',de:'de',id:'id',vi:'vi','zh-hans':'zh-Hans','zh-hant':'zh-Hant',zh:'zh'};
const norm=s=>(s||'').toLowerCase().replace(/\s+/g,'');
// 이름요소/위키미디어 내부 항목 — 실존 대상이 아니다. internal/kdb/wikidata/client.go 의
// nameElementClasses 와 같은 목록이다. 같은 규칙이 두 곳에서 달라지면 안 된다.
const NAME_ELEMENT=new Set(['Q202444','Q12308941','Q11879590','Q3409032','Q101352','Q1243157',
 'Q4167410','Q13406463','Q4167836','Q17442446','Q15184295','Q11266439','Q66087861','Q22808320','Q106589819']);
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
  if(f.length<4||!f[0]||!f[2]) return null;
  return {id:f[0],ko:f[1],qid:f[2],type:f[3]};
}).filter(Boolean);
if(rows.length===0){
  console.error('cohort.csv 에서 읽은 행이 0이다. 빈 조회를 성공으로 끝내지 않는다.');
  process.exit(2);
}
(async()=>{
  const V=[],L=[];let n=0,ok=0;
  const flush=(vp,lp)=>{
    fs.writeFileSync(vp,V.map(a=>a.map(csv).join(',')).join('\n')+(V.length?'\n':''));
    fs.writeFileSync(lp,L.map(a=>a.map(csv).join(',')).join('\n')+(L.length?'\n':''));
  };
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
    if(!got){V.push([r.id,r.qid,'FETCH_FAIL',err||'','','',r.type]);await sleep(90);continue}
    const ent=body.entities&&body.entities[r.qid];
    if(!ent||ent.missing!==undefined){V.push([r.id,r.qid,'NO_ITEM','','','',r.type]);await sleep(90);continue}
    // 재지정(redirect)된 항목은 받지 않는다 — 우리가 들고 있는 QID 가 그 대상이 아니다.
    if(ent.id&&ent.id!==r.qid){V.push([r.id,r.qid,'REDIRECTED',ent.id,'','',r.type]);await sleep(90);continue}
    const lab=ent.labels||{},ali=ent.aliases||{};
    const koL=lab.ko?lab.ko.value:'';
    const koA=(ali.ko||[]).map(a=>a.value);
    const enL=lab.en?lab.en.value:'';
    const p31=((ent.claims&&ent.claims.P31)||[]).map(c=>{try{return c.mainsnak.datavalue.value.id}catch(e){return null}}).filter(Boolean);
    const koMatch=(koL&&norm(koL)===norm(r.ko))||koA.some(a=>norm(a)===norm(r.ko));
    const isHuman=p31.indexOf('Q5')>=0;
    const isNameEl=p31.some(q=>NAME_ELEMENT.has(q));
    // ★유형을 보고 판정한다. 종전엔 모든 대상에 instance of Q5 를 요구해서
    //   장소·작품의 올바른 링크까지 NOT_HUMAN 으로 떨어뜨렸다(앞 400건에서 174건).
    //   장소의 항목이 사람이 아닌 것은 당연하다 — 유형이 어긋나는지를 본다.
    let verdict='OK';
    if(isNameEl) verdict='NAME_ELEMENT';
    else if(r.type==='person' && !isHuman) verdict='TYPE_MISMATCH_NOT_HUMAN';
    else if(r.type!=='person' && isHuman)  verdict='TYPE_MISMATCH_IS_HUMAN';
    else if(!koMatch) verdict='KO_MISMATCH';
    V.push([r.id,r.qid,verdict,koL,enL,p31.slice(0,4).join(' '),r.type]);
    if(verdict==='OK'){
      ok++;
      for(const k of Object.keys(lab)){
        const kk=LOCALES[k.toLowerCase()];
        if(kk) L.push([r.id,kk,lab[k].value]);
      }
    }
    if(n%100===0){
      console.error('  ...'+n+'/'+rows.length+'  OK='+ok);
      // ★중간 저장. 결과를 끝에만 쓰면 긴 조회가 5시간째 죽었을 때 전부 잃는다.
      //   실측: 15,092건이 ≈33건/분으로 약 6시간 걸린다. 100건마다 부분 결과를 남기고,
      //   done 표시는 정상 종료 때만 쓴다 — 부분 결과를 완료로 착각하지 않게.
      flush('/work/verdicts.partial.csv','/work/labels.partial.csv');
    }
    await sleep(90);
  }
  flush('/work/verdicts.csv','/work/labels.csv');
  try{fs.unlinkSync('/work/verdicts.partial.csv')}catch(e){}
  try{fs.unlinkSync('/work/labels.partial.csv')}catch(e){}
  const cnt={};V.forEach(a=>cnt[a[2]]=(cnt[a[2]]||0)+1);
  console.log('조회 '+rows.length+'건 — '+Object.entries(cnt).sort((a,b)=>b[1]-a[1]).map(([k,v])=>k+' '+v).join(' · '));
  console.log('라벨 행 '+L.length);
  // ★유형별로 나눠 본다. TDB 링커의 품질이 유형마다 다르다 —
  //   인물은 거의 정확한데 장소는 역 이름이 든 상호를 그 역에 걸어 놓았다.
  const byType={};
  V.forEach(a=>{const t=a[6]||'?';byType[t]=byType[t]||{};byType[t][a[2]]=(byType[t][a[2]]||0)+1});
  console.log('\n-- 유형별 --');
  Object.keys(byType).sort().forEach(t=>{
    const m=byType[t],tot=Object.values(m).reduce((x,y)=>x+y,0);
    console.log('  '+t.padEnd(12)+' 계 '+String(tot).padStart(6)+'  '+
      Object.entries(m).sort((a,b)=>b[1]-a[1]).map(([k,v])=>k+' '+v).join(' · '));
  });
})();
