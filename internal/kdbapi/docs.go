package kdbapi

import "net/http"

// docs — 공개 API 문서 페이지(무인증). 우리가 제공하는 DB 의 범위(K-콘텐츠 고유명사
// 13 type)와 클라이언트 협업 워크플로우(받기/준비/보내기/개선)를 명시한다.
func (h *handler) docs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write([]byte(docsHTML))
}

const docsHTML = `<!doctype html>
<html lang="ko"><head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>KDB API — K-콘텐츠 고유명사 다국어 DB</title>
<style>
:root{--fg:#1a1a1a;--mut:#666;--bg:#fff;--card:#f7f7f8;--line:#e3e3e6;--acc:#2b6cb0;--ok:#1f7a4d;--warn:#9a6b00}
*{box-sizing:border-box}body{font:15px/1.65 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,"Noto Sans KR",sans-serif;color:var(--fg);background:var(--bg);margin:0}
.wrap{max-width:880px;margin:0 auto;padding:32px 20px 80px}
h1{font-size:26px;margin:0 0 4px}h2{font-size:20px;margin:36px 0 10px;padding-top:10px;border-top:1px solid var(--line)}
h3{font-size:16px;margin:22px 0 6px}
.sub{color:var(--mut);margin:0 0 8px}
code{background:var(--card);padding:1px 5px;border-radius:4px;font:13px/1.5 ui-monospace,SFMono-Regular,Menlo,monospace}
pre{background:#1e1e22;color:#e6e6e6;padding:14px 16px;border-radius:8px;overflow:auto;font:13px/1.55 ui-monospace,SFMono-Regular,Menlo,monospace}
pre .k{color:#9cdcfe}pre .s{color:#ce9178}pre .c{color:#6a9955}
table{border-collapse:collapse;width:100%;margin:10px 0;font-size:14px}
th,td{border:1px solid var(--line);padding:7px 10px;text-align:left;vertical-align:top}
th{background:var(--card)}
.pill{display:inline-block;background:var(--card);border:1px solid var(--line);border-radius:999px;padding:1px 9px;margin:2px 3px 2px 0;font-size:13px}
.flow{display:flex;gap:8px;flex-wrap:wrap;margin:10px 0}
.flow .step{flex:1;min-width:170px;background:var(--card);border:1px solid var(--line);border-radius:8px;padding:10px 12px}
.flow .step b{color:var(--acc)}
.note{background:#f0f6ff;border-left:3px solid var(--acc);padding:8px 12px;border-radius:0 6px 6px 0;margin:10px 0}
.ok{color:var(--ok)}.warn{color:var(--warn)}
a{color:var(--acc)}
</style></head><body><div class="wrap">

<h1>KDB API</h1>
<p class="sub">한국 K-콘텐츠 고유명사의 <b>현지 통용 다국어 표기/번역</b>을 제공하는 API.
현지 매체가 실제로 쓰는 표기를 외부에 제공하는 것이 목적입니다.</p>

<h2>1. 우리가 제공하는 DB</h2>
<p>KDB 는 <b>K-엔터테인먼트 고유명사</b>만 다룹니다 — 한국 대중문화(K-pop·드라마·영화·예능)에
등장하는 인물·작품·조직 등의 <b>현지 통용 다국어 표기/번역</b>입니다. 아래 표의 범주에
해당하는 고유명사만 요청하세요. 범위 밖은 <code>out_of_scope</code> 로 응답하고 등록하지
않습니다(도메인 품질 보호).</p>

<p>다루는 <b>entity_type</b> — 각 type 별 제공 내용과 요청 가능한 고유명사 예시:</p>
<table>
<tr><th>type</th><th>제공 내용</th><th>예시(요청 가능한 고유명사)</th></tr>
<tr><td><code>person</code> 인물</td><td>배우·가수·아이돌·감독·예능인·MC 등 실존 인물<br><span class="sub">+ 소속사·대표작·출생연도·별칭 제공</span></td><td>박보검, 아이유, 뷔, 봉준호</td></tr>
<tr><td><code>group</code> 그룹</td><td>아이돌·밴드·유닛 등 그룹</td><td>방탄소년단, 뉴진스, 르세라핌, 소녀시대</td></tr>
<tr><td><code>drama</code> 드라마</td><td>한국 드라마 (공식 현지 제목)</td><td>오징어 게임, 폭싹 속았수다, 신사와 아가씨</td></tr>
<tr><td><code>movie</code> 영화</td><td>한국 영화 (공식 현지 제목)</td><td>기생충, 헤어질 결심</td></tr>
<tr><td><code>show</code> 예능</td><td>예능·교양 프로그램</td><td>나 혼자 산다, 유 퀴즈 온 더 블럭, 1박2일</td></tr>
<tr><td><code>song_album</code> 곡/앨범</td><td>노래·앨범 타이틀</td><td>Dynamite, 좋은 날</td></tr>
<tr><td><code>agency</code> 소속사</td><td>엔터테인먼트 기획사</td><td>하이브, JYP, 스타쉽엔터테인먼트</td></tr>
<tr><td><code>channel_outlet</code> 방송사/매체</td><td>방송사·채널·미디어</td><td>tvN, JTBC, Mnet, MBC에브리원</td></tr>
<tr><td><code>brand_place</code> 브랜드/장소</td><td>K-콘텐츠 연관 브랜드·촬영지·명소</td><td>(작품·스타 연관 브랜드/장소)</td></tr>
<tr><td><code>event_tour</code> 행사/투어</td><td>시상식·페스티벌·콘서트 투어</td><td>멜론 뮤직 어워드, 백상예술대상, KBS 가요대축제</td></tr>
<tr><td><code>character</code> 캐릭터</td><td>드라마·영화·웹툰 등장인물</td><td>(작품 속 배역명)</td></tr>
<tr><td><code>term</code> 용어</td><td>K-콘텐츠 고유 용어·현상 (일반어 아님)</td><td>한류</td></tr>
</table>
<p class="sub">※ 보유 항목 수는 매일 늘어납니다. 고정 숫자가 아니므로 실시간 규모는
<code>GET /v1/health</code>(<code>entities</code> 필드) 로 확인하세요.</p>

<div class="note warn"><b>범위 밖 — 요청하지 마세요(<code>out_of_scope</code> 로 회신, 등록 안 함).</b>
해외(비-K) 인물·작품, 일반 행정지명(서울·부산), 일반 기업·제품, 일반 명사(<code>김치</code>·<code>컴백</code>),
문장·명령형(<code>"점심메뉴추천해줘"</code>), 키보드 난수·깨진 자소.</div>
<div class="note"><b>입력 규칙 — 깨끗한 고유명사 하나만(오염 방지).</b> term 은 위 범주의
<b>고유명사 한 개</b>여야 합니다. 문장·서술 구절을 통째로 넣지 마세요. type 힌트
(<code>{"ko":..,"type":".."}</code>)를 주면 매칭·동명이인 정확도가 올라갑니다.</div>

<p>제공 <b>locale</b> (9): <code>ko</code>(원문) · <code>en</code> · <code>ja</code> · <code>zh</code>(간체) ·
<code>zh_hant</code>(번체) · <code>vi</code> · <code>es</code> · <code>id</code> · <code>pt_br</code></p>
<div class="note"><b>표기 vs 번역.</b> 인물·그룹은 <b>현지 음역</b>(예: 박보검 → ja <code>パク・ボゴム</code>,
zh <code>朴宝剑</code>)을, 드라마·영화·예능·곡은 <b>공식 현지 제목(번역)</b>(예: 오징어 게임 →
en <code>Squid Game</code>, es <code>El juego del calamar</code>, pt_br <code>Round 6</code>)을 제공합니다.</div>

<h2>2. 협업 워크플로우</h2>
<p>단방향 제공이 아니라, 클라이언트와 교류하며 품질을 올립니다.</p>
<div class="flow">
<div class="step"><b>① 받기/준비</b><br>기사 작성 시점에 고유명사(한글)를 <code>/v1/prepare</code> 로 미리 던지면, 조회 전에 번역을 준비합니다.</div>
<div class="step"><b>② 보내기</b><br><code>/v1/lookup</code>·<code>/v1/entities/match</code> 로 완성된 다국어 표기를 조회합니다.</div>
<div class="step"><b>③ 개선</b><br>오역을 발견하면 <code>/v1/corrections</code> 로 신고 → 검증되면 자동 반영됩니다.</div>
</div>

<h2>3. 인증</h2>
<p>모든 <code>/v1/*</code> 요청은 API 키가 필요합니다(헤더). <code>/docs</code>·<code>/v1/health</code> 는 무인증.</p>
<pre><span class="k">X-KDB-Key</span>: &lt;your-api-key&gt;   <span class="c"># 또는 Authorization: Bearer &lt;key&gt;</span></pre>

<h2>4. 엔드포인트</h2>

<h3>POST /v1/prepare — 받기 + 빠른 준비</h3>
<p>기사에 등장할 한글 고유명사를 미리 던지면 KDB 가 그 사이 번역을 준비합니다(사람 개입 없음).
각 term 은 문자열 또는 <code>{ko,type}</code>. <b>type 힌트(선택)</b>는 매칭·동명이인 정확도를 높입니다.
<b>source_url(선택)</b>은 출처 기사 URL — batch 레벨(기사당 1개) 또는 term별로 넣으면 추적·역추적에 씁니다.</p>
<pre>POST /v1/prepare
{ <span class="k">"terms"</span>: [
    {<span class="k">"ko"</span>:<span class="s">"박보검"</span>, <span class="k">"type"</span>:<span class="s">"person"</span>},
    {<span class="k">"ko"</span>:<span class="s">"폭싹 속았수다"</span>, <span class="k">"type"</span>:<span class="s">"drama"</span>},
    <span class="s">"아이유"</span>
  ],
  <span class="k">"locales"</span>: [<span class="s">"ja"</span>,<span class="s">"zh"</span>,<span class="s">"es"</span>],   <span class="c">// 생략 시 9개 전체</span>
  <span class="k">"source_url"</span>: <span class="s">"https://kstory.aiinplanet.com/…"</span>   <span class="c">// 선택: 출처 기사(추적)</span>
}
→ { <span class="k">"items"</span>: [
    { <span class="k">"term"</span>:<span class="s">"박보검"</span>, <span class="k">"status"</span>:<span class="s">"ready"</span>, <span class="k">"type"</span>:<span class="s">"person"</span>,
      <span class="k">"values"</span>:{<span class="k">"ja"</span>:<span class="s">"パク・ボゴム"</span>,<span class="k">"zh"</span>:<span class="s">"朴宝剑"</span>,<span class="k">"es"</span>:<span class="s">"Park Bo-gum"</span>} },
    { <span class="k">"term"</span>:<span class="s">"폭싹 속았수다"</span>, <span class="k">"status"</span>:<span class="s">"preparing"</span>, <span class="k">"missing"</span>:[<span class="s">"es"</span>] },
    { <span class="k">"term"</span>:<span class="s">"아이유"</span>, <span class="k">"status"</span>:<span class="s">"ready"</span>, ... } ] }</pre>
<table>
<tr><th>status</th><th>의미</th></tr>
<tr><td class="ok">ready</td><td>요청 locale 다 준비됨 — 즉시 사용 가능</td></tr>
<tr><td>preparing</td><td>빈 locale 을 백그라운드로 준비 시작 — 잠시 후 조회하면 채워짐</td></tr>
<tr><td>new</td><td>처음 보는 고유명사 — 발굴·분류 파이프라인 진입(K-콘텐츠면 준비)</td></tr>
<tr><td class="warn">out_of_scope</td><td>K-콘텐츠가 아니거나 노이즈 — 준비/등록하지 않음</td></tr>
</table>

<h3>POST /v1/entities/match — 본문에서 매칭(번역 핫패스)</h3>
<p>기사 본문에서 알려진 엔티티를 찾아 목표 locale 표기로 매핑합니다.</p>
<pre>POST /v1/entities/match
{ <span class="k">"source_text"</span>:<span class="s">"방탄소년단의 새 앨범…"</span>, <span class="k">"locale"</span>:<span class="s">"ja"</span>,
  <span class="k">"min_confidence"</span>:0.9, <span class="k">"verified_only"</span>:true }</pre>
<table>
<tr><th>응답 필드</th><th>설명</th></tr>
<tr><td>locale_name</td><td>해당 locale 표기/번역</td></tr>
<tr><td>provenance</td><td>반환값의 출처 등급: <code>operator-locked</code> · <code>wikidata-label</code> · <code>external-db</code> · <code>media-consensus</code> · <code>wikipedia-langlinks</code> · <code>media-single</code> · <code>llm-only</code></td></tr>
<tr><td>locale_source</td><td>그 값의 raw source(소비자 자체 게이팅용)</td></tr>
</table>
<div class="note"><code>verified_only:true</code> 는 <b>반환되는 locale 값 자체</b>가 검증 소스일 때만 반환합니다
(en 은 wikidata 인데 ja 는 LLM 합성인 경우, <code>locale=ja</code> 로는 ja 출처만 봅니다).</div>

<h3>POST /v1/corrections — 오역 정정 신고(개선)</h3>
<p>KDB 가 잘못된 표기를 줬을 때 올바른 값을 근거와 함께 신고합니다. <b>모든 신고는 접수되어
KDB 가 검토</b>합니다(임의 거부 없음 — 여러분의 오류 지적을 존중). 단, <b>자동 반영은 권위
외부소스(Wikidata)가 독립 확인한 경우에만</b> 이뤄지므로 단일 클라이언트가 임의로 데이터를
바꿀 수 없습니다. 문자셋 의심·미보유 고유명사도 거부하지 않고 각각 운영자 검토·발굴 큐로 받습니다.</p>
<pre>POST /v1/corrections
{ <span class="k">"ko"</span>:<span class="s">"박보검"</span>, <span class="k">"locale"</span>:<span class="s">"ja"</span>,
  <span class="k">"returned"</span>:<span class="s">"パクボゴム"</span>, <span class="k">"suggested"</span>:<span class="s">"パク・ボゴム"</span>,
  <span class="k">"evidence_url"</span>:<span class="s">"https://ja.wikipedia.org/wiki/パク・ボゴム"</span> }</pre>
<table>
<tr><th>result.status</th><th>의미</th></tr>
<tr><td class="ok">auto_applied</td><td>Wikidata 일치 → 즉시 반영(<code>value</code> 회신)</td></tr>
<tr><td>verifying</td><td>Wikidata 로 판정 안 됨 → KDB 가 codex 로 검증 중. <code>GET /v1/corrections/{correction_id}</code> 로 결과 확인</td></tr>
<tr><td>queued</td><td>근거 미달/불확실·보호된 값·문자셋 의심·미보유 고유명사 → 접수 후 운영자 검토(또는 발굴). 신고는 버려지지 않음</td></tr>
</table>
<div class="note"><b>양방향 검증(KDB가 판단·회신 → 클라가 확인).</b> Wikidata 로 즉시 확인 안 되면
KDB 가 codex 로 내용을 검증합니다(<code>verifying</code>). 결과는 폴링으로 확인:</div>
<pre>GET /v1/corrections/{correction_id}
→ { <span class="k">"result"</span>:{ <span class="k">"status"</span>:<span class="s">"proposed"</span>, <span class="k">"proposed"</span>:<span class="s">"パク・ボゴム"</span>, ... } }
   <span class="c"># auto_applied=반영됨 / proposed=KDB 수정안(확인 필요) / rejected / queued</span></pre>
<p>KDB 가 더 정확한 값을 알면 <code>proposed</code> 로 수정안을 회신합니다. 동의하면 확인:</p>
<pre>POST /v1/corrections
{ <span class="k">"confirm_id"</span>: 1234, <span class="k">"accept"</span>: true }
→ { <span class="k">"result"</span>:{ <span class="k">"status"</span>:<span class="s">"auto_applied"</span>, <span class="k">"value"</span>:<span class="s">"..."</span> } }</pre>

<h3>기타 조회</h3>
<table>
<tr><td><code>POST /v1/lookup</code></td><td>한글명으로 단건 검색</td></tr>
<tr><td><code>GET /v1/entities?q=&type=&updated_since=</code></td><td>목록/델타 동기화</td></tr>
<tr><td><code>GET /v1/entities/{id}</code></td><td>단건 상세</td></tr>
<tr><td><code>GET /v1/health</code></td><td>상태(무인증)</td></tr>
</table>

<h2>5. 권장 사용 패턴</h2>
<ol>
<li>기사 작성 시: 등장 고유명사를 <code>/v1/prepare</code> 로 던져 미리 준비.</li>
<li>발행/번역 시: <code>/v1/entities/match</code>(<code>verified_only</code>)로 조회. <code>llm-only</code> 는 자체 검증 후 사용.</li>
<li>오역 발견 시: <code>/v1/corrections</code> 로 신고 → 품질이 지속 향상됩니다.</li>
<li>주기적으로 <code>GET /v1/entities?updated_since=&lt;마지막동기화&gt;</code> 로 개선분 델타 동기화.</li>
</ol>

<p class="sub" style="margin-top:40px">문의: 운영자 발급 API 키 필요. 빈 결과는 에러가 아니라 빈 배열로 반환됩니다.</p>
</div></body></html>`
