// kowiki-en.js — 흡수분의 **en 표기를 ko.wikipedia 에서 회수**한다.
//
// 왜 kowiki 인가. 흡수분 537,841건에는 보강 어댑터가 하나도 없다(어댑터 22개는 전부
// kwave_entities 전용). 그리고 인물 16,685명은 QID 자체가 없어 위키데이터에 물을 수도 없다.
// ko.wikipedia 는 **이름으로** 찾을 수 있고, 문서 하나가 QID 와 영어판 제목을 함께 준다.
//
// 한 번의 호출로 셋을 받는다 — pageprops(QID·동음이의) · langlinks(en 제목) · extract(도입부).
// 키 없음, 쿼터 없음, 대상당 1회.
//
// ★가드는 internal/kdb/kowiki_anchor_drain.go 가 실측으로 세운 것과 같다.
//   정확일치만으로는 정밀도 38% 였고, 실패가 전부 탐지 가능한 세 유형이었다:
//     ① 동음이의 문서  ② 다른 이름으로 리다이렉트  ③ 의미 불일치
//   ③은 영문 description 이 아니라 **문서 도입부**로 본다(설명문은 정답까지 죽인다).
//
// 입력  /work/cohort.csv    entity_id,canonical_ko,entity_type
// 출력  /work/en_out.csv    entity_id,qid,en_title,verdict
//       100건마다 en_out.partial.csv — 긴 조회가 중간에 죽어도 잃지 않는다.
const fs=require('fs');
const UA='kdb-kowiki-en/1.0 (https://kdb.aiinplanet.com) node/20';
const sleep=ms=>new Promise(r=>setTimeout(r,ms));
const csv=s=>'"'+String(s==null?'':s).replace(/"/g,'""')+'"';
function splitCSV(line){const o=[];let c='',q=false;for(let i=0;i<line.length;i++){const ch=line[i];
  if(q){if(ch==='"'){if(line[i+1]==='"'){c+='"';i++}else q=false}else c+=ch}
  else if(ch==='"')q=true; else if(ch===','){o.push(c);c=''} else c+=ch}o.push(c);return o}
const raw=fs.readFileSync('/work/cohort.csv','utf8').trim();
const rows=(raw===''?[]:raw.split('\n')).map(l=>{const f=splitCSV(l);
  return (f.length>=3&&f[0]&&f[1])?{id:f[0],ko:f[1],type:f[2]}:null}).filter(Boolean);
if(rows.length===0){console.error('cohort.csv 가 비었다. 빈 조회를 성공으로 끝내지 않는다.');process.exit(2)}

// 한국 대중문화·지리 맥락. 도입부에 이것이 없으면 다른 뜻의 문서다.
const CTX=/(한국|대한민국|남한|서울|부산|대구|인천|광주|대전|울산|세종|경기|강원|충청|충북|충남|전라|전북|전남|경상|경북|경남|제주|조선|고려|신라|백제|가수|배우|그룹|아이돌|드라마|영화|예능|방송|음반|앨범|축구|야구|농구|배구|선수|감독|기업|대학교|학교|박물관|미술관|공원|사찰|사지|절|산|강|호수|섬|폭포|해수욕장|시장|역|문화재|국보|보물|사적|천연기념물)/;
const NAME_ELEMENT=/(이름|성씨|인명)/;

(async()=>{
  const O=[];let n=0,pass=0;
  const flush=p=>fs.writeFileSync(p,O.map(a=>a.map(csv).join(',')).join('\n')+(O.length?'\n':''));
  for(const r of rows){
    n++;
    let j=null;
    try{
      const u='https://ko.wikipedia.org/w/api.php?action=query&format=json&redirects=1'
        +'&prop=pageprops%7Cextracts%7Clanglinks&ppprop=disambiguation%7Cwikibase_item'
        +'&exintro=1&explaintext=1&lllang=en&titles='+encodeURIComponent(r.ko);
      const res=await fetch(u,{headers:{'User-Agent':UA}});
      if(res.ok) j=await res.json();
      else { O.push([r.id,'','','HTTP_'+res.status]); await sleep(110); continue }
    }catch(e){ O.push([r.id,'','','NET']); await sleep(300); continue }
    const pages=j&&j.query&&j.query.pages?Object.values(j.query.pages):[];
    const p=pages[0];
    if(!p||p.missing!==undefined){ O.push([r.id,'','','NO_PAGE']); await sleep(110); continue }
    const pp=p.pageprops||{};
    // ① 동음이의 문서
    if(pp.disambiguation!==undefined){ O.push([r.id,'','','DISAMBIG']); await sleep(110); continue }
    // ② 리다이렉트 — 해소된 제목이 원어와 같아야 한다(괄호 주석만 붙는 건 허용)
    const title=(p.title||'').replace(/\s*\([^)]*\)\s*$/,'').trim();
    if(title!==r.ko){ O.push([r.id,'','','REDIRECT:'+(p.title||'')]); await sleep(110); continue }
    const intro=(p.extract||'').slice(0,500);
    // ③ 의미 불일치 — 도입부에 한국 맥락이 있어야 한다. 이름요소 문서도 제외.
    if(NAME_ELEMENT.test(intro.slice(0,60))){ O.push([r.id,'','','NAME_ELEMENT']); await sleep(110); continue }
    if(!CTX.test(intro)){ O.push([r.id,'','','NO_CONTEXT']); await sleep(110); continue }
    const en=(p.langlinks&&p.langlinks[0]&&p.langlinks[0]['*'])||'';
    const qid=pp.wikibase_item||'';
    if(!en){ O.push([r.id,qid,'','NO_EN']); await sleep(110); continue }
    O.push([r.id,qid,en,'OK']); pass++;
    if(n%100===0){ console.error('  ...'+n+'/'+rows.length+'  OK='+pass); flush('/work/en_out.partial.csv') }
    await sleep(110);
  }
  flush('/work/en_out.csv');
  try{fs.unlinkSync('/work/en_out.partial.csv')}catch(e){}
  const c={};O.forEach(a=>{const k=a[3].split(':')[0];c[k]=(c[k]||0)+1});
  console.log('조회 '+rows.length+'건 — '+Object.entries(c).sort((a,b)=>b[1]-a[1]).map(([k,v])=>k+' '+v).join(' · '));
  console.log('en 회수 '+pass+'건 ('+(100*pass/rows.length).toFixed(1)+'%) · 그중 QID 동반 '+O.filter(a=>a[3]==='OK'&&a[1]).length);
})();
