package kdbapi

import (
	"html"
	"net/http"
	"strings"
)

// docs — 공개 API 문서 페이지(무인증). 우리가 제공하는 DB 의 범위(K-콘텐츠 고유명사
// 13 type)와 클라이언트 협업 워크플로우(받기/준비/보내기/개선)를 명시한다.
func (h *handler) docs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	// ★규칙 변경 이력은 **손으로 두 벌 적지 않는다** (2026-09-16). 이 페이지와
	//   /v1/changelog 가 같은 표(changelog.go ruleChanges)에서 만들어진다. 두 벌이면
	//   한쪽이 뒤처지고, 뒤처진 쪽을 소비자가 읽는다.
	page := strings.Replace(docsHTML, ruleChangesSlot, renderRuleChangesHTML(), 1)
	page = strings.Replace(page, rulesVersionSlot, html.EscapeString(CurrentRuleVersion()), -1)
	_, _ = w.Write([]byte(page))
}

// 본문 안의 자리표. 상수로 둬서 오타가 나면 시험이 잡는다.
const (
	ruleChangesSlot  = "<!--RULE-CHANGES-->"
	rulesVersionSlot = "<!--RULES-VERSION-->"
)

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
<p>KDB 는 <b>한국 대상 고유명사</b>를 다룹니다 — 한국의 인물·작품·조직·기관의
<b>현지 통용 다국어 표기/번역</b>입니다. 아래 표의 범주에 해당하는 고유명사를 요청하세요.
범위 밖은 <code>out_of_scope</code> 로 응답하고 등록하지 않습니다(도메인 품질 보호).</p>

<div class="note warn"><b>★ 2026-09-15 범위 확대.</b> 종전에는 <b>K-엔터테인먼트만</b>
다뤘습니다(K-pop·드라마·영화·예능). 지금은 <b>분야를 묻지 않습니다</b> —
정치·경제·시사·스포츠·학계·언론 모두 받습니다.
<br>같은 날 새 유형 6종을 열었습니다: <code>political_party</code> ·
<code>government_body</code> · <code>company</code> · <code>organization</code> ·
<code>sports_team</code> · <code>school</code>.
<br><b>정치인·운동선수·기업인·학자는 모두 <code>person</code></b> 이고, 무슨 영역인지는
응답의 <code>occupation_domain</code> 이 알려 줍니다.
<br><span class="sub">종전 범위로 기각돼 있던 항목(이재명·차범근·서울대학교 등)은 순차
복구 중입니다. 같은 낱말을 다시 물으면 지금 규칙으로 새로 판단합니다 —
옛 <code>out_of_scope</code> 종결은 규칙이 바뀌면서 만료됐습니다.</span></div>

<div class="note"><b>고유명사를 고르는 일은 보내는 쪽이 합니다.</b>
KDB 는 기사를 읽어 고유명사를 뽑아내지 않습니다 — <b>지목해서 보내 주신 것만</b> 받습니다.
기사 본문을 통째로 보내면 이미 아는 것과 겹치는 부분만 돌려드릴 수 있고, 무엇을 물으신
것인지 우리가 짐작하지 않습니다. 기사에서 고유명사를 분리해
<code>POST /v1/lookup/bulk</code> 의 <code>queries</code> 에 <b>이름과 유형을 붙여</b> 보내주세요
(같은 이름이 둘일 때 가릴 재료가 됩니다).
<br><span class="sub">종전에는 우리가 웹에서 관련 페이지를 찾아 표기를 보충했습니다. 그 경로에서
요청하지 않은 페이지의 낱말까지 원장에 들어오는 일이 있어 2026-09-15 에 닫았습니다.</span></div>

<div class="note"><b>★ kid — KDB 자체 ID. 이름 대신 이것으로 물으세요.</b>
<br>모든 응답에 <code>kid</code>(예: <code>K0004821</code>)가 실려 나갑니다. 한 번 받은 kid 를
저장해 두고 다음부터 그것으로 물으면 <b>동명이인이 사라집니다</b> — 이름은 겹치지만 kid 는 안 겹칩니다.
<br>이름이 바뀌어도(개명·활동명 변경·줄임말) <b>같은 kid</b> 입니다. 한 대상 = 하나의 kid 이고,
그 대상의 모든 호칭(본명·활동명·줄임말·로마자)은 그 kid 안의 별칭입니다.
<pre>POST /v1/lookup  {"kid": "K0004821"}
POST /v1/lookup  {"query": "K0004821"}   // query 자리에 넣어도 같습니다</pre>
kid 로 물으면 <b>확정 한 건</b>을 돌려줍니다 — 후보 목록도, 발굴 대기도 없습니다.
<br><span class="sub">위키데이터 QID 는 <b>보조 근거</b>일 뿐 우리 ID 가 아닙니다. 주 앵커는 kid 입니다.</span></div>

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
<tr><td colspan="3" style="background:#f6f7f9"><b>정치·경제·시사·스포츠</b> (2026-09-15 추가)</td></tr>
<tr><td><code>political_party</code> 정당</td><td>정당·원내교섭단체</td><td>더불어민주당, 국민의힘, 조국혁신당</td></tr>
<tr><td><code>government_body</code> 정부·공공기관</td><td>부처·청·위원회·공사·공단·지자체·국회·법원</td><td>기획재정부, 금융감독원, 한국전력공사, 서울특별시</td></tr>
<tr><td><code>company</code> 기업</td><td>일반 기업·법인 (연예기획사는 <code>agency</code>)</td><td>삼성전자, 현대자동차, 네이버, 카카오</td></tr>
<tr><td><code>organization</code> 협회·단체</td><td>협회·재단·노조·학회·연맹</td><td>대한체육회, 한국프로축구연맹, 전국경제인연합회</td></tr>
<tr><td><code>sports_team</code> 스포츠 구단</td><td>프로 구단·국가대표팀</td><td>FC서울, 두산 베어스, 대한민국 축구 국가대표팀</td></tr>
<tr><td><code>school</code> 학교</td><td>학교·대학</td><td>서울대학교, 한국예술종합학교</td></tr>
<tr><td colspan="3" style="background:#f6f7f9"><b>작품 — 기각 더미에서 실제로 들어오던 것</b> (2026-09-15 추가)</td></tr>
<tr><td><code>game</code> 게임</td><td>모바일·PC·콘솔 게임 (나라별 <b>스토어 공식 제목</b> 제공)</td><td>리니지W, 블루 아카이브, 승리의 여신: 니케, P의 거짓</td></tr>
<tr><td><code>musical_play</code> 뮤지컬·연극</td><td><b>작품 자체</b> (그 공연 회차는 <code>event_tour</code>)</td><td>레 미제라블, 노트르담 드 파리, 몬테크리스토</td></tr>
<tr><td><code>webtoon</code> 웹툰·웹소설</td><td>웹툰·웹소설·만화</td><td>복학왕, 나 혼자만 레벨업</td></tr>
<tr><td><code>publication</code> 잡지·도서</td><td>잡지·단행본</td><td>쎄씨, 뷰티쁠</td></tr>
</table>
<div class="note"><b>게임은 앱스토어에서 공식 제목을 가져옵니다.</b> 위키데이터에는 게임 제목이
거의 없지만, 스토어에는 <b>퍼블리셔가 직접 등록한 나라별 제목</b>이 있습니다.
<pre>블루 아카이브   ja ブルーアーカイブ   en Blue Archive   zh_hant 蔚藍檔案
승리의 여신: 니케  ja 勝利の女神：NIKKE   en GODDESS OF VICTORY: NIKKE</pre>
<span class="sub">그 나라에 미출시면 <b>빈칸</b>으로 답합니다 — 지어내지 않습니다.
<code>붉은사막</code>·<code>마비노기 모바일</code> 처럼 국내 전용인 경우입니다.</span></div>
<div class="note"><b>기사에서 유형을 고르는 단서</b> — 본문에 아래 낱말이 있으면 그 유형입니다.
<table>
<tr><th>type</th><th>본문 단서</th></tr>
<tr><td><code>political_party</code></td><td>정당 · 여당 · 야당 · 원내대표 · 당대표 · 창당 · 비례대표</td></tr>
<tr><td><code>government_body</code></td><td>부처 · ○○청 · ○○위원회 · 공단 · 공사 · 공공기관 · 지자체 · 국회 · 법원 · 검찰</td></tr>
<tr><td><code>company</code></td><td>기업 · 회사 · 법인 · 주식회사 · 계열사 · 상장 · 코스피 · 코스닥</td></tr>
<tr><td><code>organization</code></td><td>협회 · 재단 · 단체 · 학회 · 노조 · 조합 · 연맹 · 사단법인</td></tr>
<tr><td><code>sports_team</code></td><td>구단 · 프로팀 · 국가대표팀 · 선수단 · FC · 이글스/라이온즈/베어스류</td></tr>
<tr><td><code>school</code></td><td>학교 · 대학 · 대학교 · 고등학교 · 캠퍼스 · 대학원</td></tr>
<tr><td><code>game</code></td><td>게임 · 모바일게임 · 출시 · 업데이트 · 서버 · 길드 · 던전 · RPG · 넥슨/엔씨/넷마블/크래프톤</td></tr>
<tr><td><code>musical_play</code></td><td>뮤지컬 · 연극 · 초연 · 재연 · 넘버 · 극장 · 예술의전당</td></tr>
<tr><td><code>webtoon</code></td><td>웹툰 · 웹소설 · 연재 · 작화 · 네이버웹툰 · 카카오페이지 · 원작</td></tr>
<tr><td><code>publication</code></td><td>잡지 · 월간 · 화보 · 표지 · 단행본 · 출간</td></tr>
<tr><td><code>person</code></td><td>사람을 가리키는 모든 직함 — 배우·가수뿐 아니라 <b>의원·장관·대표이사·선수·감독·교수·기자</b> 도 포함</td></tr>
</table>
<span class="sub">※ <code>agency</code>(연예기획사)와 <code>company</code>(일반 기업)는 다릅니다 —
하이브·JYP 는 <code>agency</code>, 삼성전자·네이버는 <code>company</code> 입니다.
<code>channel_outlet</code>(방송사·매체)과 <code>company</code> 도 다릅니다 — JTBC 는 <code>channel_outlet</code> 입니다.</span></div>

<div class="note"><b>새 유형의 표기 방식</b> — 기관·기업·구단·학교는 <b>공식 영문명</b>이 있으면
그것을 그대로 씁니다(음역하지 않습니다). 라틴 문자권(es·vi·id·pt-br)은 영문 표기를 그대로 승계합니다.
<pre>삼성전자    en Samsung Electronics   ja サムスン電子   zh 三星电子
기획재정부   en Ministry of Economy and Finance
FC서울      en FC Seoul              ja FCソウル
서울대학교   en Seoul National University</pre>
<span class="sub">공식 영문명이 없으면 로마자 표기로 채우고 <code>locale_provenance</code> 에 그 출처를 밝힙니다.</span></div>

<div class="note"><b>정치인·운동선수·기업인도 <code>person</code> 입니다.</b> 유형을 늘리지 않았습니다 —
배우 겸 정치인을 어느 칸에 넣을지 정할 수 없기 때문입니다. 대신 응답에
<code>occupation_domain</code> 을 실어 무슨 영역의 사람인지 알려드립니다:
<code>entertainment</code> · <code>sports</code> · <code>politics</code> · <code>business</code> ·
<code>media</code> · <code>academia</code> · <code>arts</code>.
근거는 위키데이터 P106(직업)이며, <b>모르면 빈 문자열</b>입니다(지어내지 않습니다).
<br>직업이 여럿인 사람은 <b>가장 많은 영역</b>으로 답하고, 원자료(P106 전부)는 원장에 남깁니다.
<br><code>gender</code>(<code>male</code>·<code>female</code>·<code>other</code>)도 같이 나갑니다 —
근거는 위키데이터 P21 이고 <b>이름에서 추정하지 않습니다</b>(지민·현우·서연은 다 양성입니다).
모르면 빈 문자열입니다.
<br><b>같은 이름 다른 사람</b>을 가르는 데 이 둘이 쓰입니다:
<code>박찬호 [person · sports]</code> vs <code>박찬호 [person · entertainment]</code>.</div>
<p class="sub">※ 보유 항목 수는 매일 늘어납니다. 고정 숫자가 아니므로 실시간 규모는
<code>GET /v1/health</code>(<code>entities</code> 필드) 로 확인하세요.</p>

<div class="note warn"><b>범위 밖 — 요청하지 마세요(<code>out_of_scope</code> 로 회신, 등록 안 함).</b>
해외(비-한국) 인물·작품·조직, 일반 명사(<code>김치</code>·<code>컴백</code>·<code>경제성장률</code>),
문장·명령형(<code>"점심메뉴추천해줘"</code>), 키보드 난수·깨진 자소.
<br><b>★2026-09-15 범위 확대.</b> 종전에는 "일반 기업·제품"과 "행정지명"도 범위 밖이었습니다.
정치·경제·시사·스포츠 기사를 다루게 되면서 <b>한국의 기업·정부기관·정당·구단·학교·지자체는
정상 요청 대상</b>이 되었습니다(위 표의 새 6종). 다만 <b>제품·서비스명</b>과 <b>일반 경제용어</b>는
여전히 범위 밖입니다 — 갤럭시 S25 는 제품이고 삼성전자는 <code>company</code> 입니다.
<br><b>★실측 위반 사례(2026-07-17, 실제 유입분)</b> — 한국 기사에 등장해도 <b>한국 대상이
아니면</b> 보내지 마세요: 해외 배우·감독(<code>크리스토퍼 놀런</code>·<code>라이언 고슬링</code>·<code>제임스 캐머런</code>),
해외 작품 캐릭터(<code>로키</code>), J-pop(<code>오모이노타케</code>·<code>M!LK</code>), 해외 서비스(<code>그록</code>).
이런 키워드는 번역 DB 에 등록되지 않고 <b>보류 큐에 최장 21일 잡혀 그 소비자의 미해결 지표만
쌓입니다</b>(실측: 크리스토퍼 놀런 4회 재요청 → 전부 보류). 판별 기준은 <b>"한국의 인물·작품·조직·기관인가"</b>입니다(종전 "한국 대중문화의 ~"에서
2026-09-15 확대) — <b>한국에서 활동하는 외국 국적 멤버(예: 니쥬 한국계 멤버, K-그룹의
외국인 멤버)는 K-엔티티가 맞으므로</b> 정상 요청 대상입니다.</div>
<div class="note"><b>입력 규칙 — 깨끗한 고유명사 하나만(오염 방지).</b> term 은 위 범주의
<b>고유명사 한 개</b>여야 합니다. 문장·서술 구절을 통째로 넣지 마세요. type 힌트
(<code>{"ko":..,"type":".."}</code>)를 주면 매칭·동명이인 정확도가 올라갑니다.</div>

<p>제공 <b>locale</b> (9): <code>ko</code>(원문) · <code>en</code> · <code>ja</code> · <code>zh</code>(간체) ·
<code>zh_hant</code>(번체) · <code>vi</code> · <code>es</code> · <code>id</code> · <code>pt_br</code></p>
<div class="note"><b>표기 vs 번역.</b> 인물·그룹은 <b>현지 음역</b>(예: 박보검 → ja <code>パク・ボゴム</code>,
zh <code>朴宝剑</code>)을, 드라마·영화·예능·곡은 <b>공식 현지 제목(번역)</b>(예: 오징어 게임 →
en <code>Squid Game</code>, es <code>El juego del calamar</code>, pt_br <code>Round 6</code>)을 제공합니다.</div>

<h2>2. 신원 모델 — 같은 이름이 여러 개일 때</h2>
<p>이름은 <b>키가 아닙니다.</b> 같은 이름의 다른 대상은 각자 자기 <code>id</code> 를 갖습니다.
번역에서 가장 많이 틀리는 자리가 여기입니다.</p>
<pre><span class="c">// 같은 "채영" 인데 다른 사람입니다</span>
{ <span class="k">"ko"</span>:<span class="s">"채영"</span>, <span class="k">"id"</span>:<span class="s">"84ab95a9-…"</span>, <span class="k">"disambig"</span>:<span class="s">"(TWICE)"</span>, <span class="k">"ja"</span>:<span class="s">"チェヨン"</span> }
{ <span class="k">"ko"</span>:<span class="s">"채영"</span>, <span class="k">"id"</span>:<span class="s">"2dd14daf-…"</span>, <span class="k">"disambig"</span>:<span class="s">"(CLC)"</span>,   <span class="k">"ja"</span>:<span class="s">"チェヨン"</span> }</pre>
<table>
<tr><th>필드</th><th>무엇인가</th><th>어떻게 쓰나</th></tr>
<tr><td><code>id</code></td><td><b>신원 표식.</b> 한 대상 = 하나의 id. 겸업으로 갈리지 않습니다(가수이자 배우여도 하나).</td><td>기사에서 한 번 정한 대상을 <b>저장해 두고 다음부터 id 로 조회</b>하세요. 표기가 같아도 id 가 다르면 다른 대상입니다.</td></tr>
<tr><td><code>disambig</code></td><td><b>표시 한정어.</b> <code>(가수)</code>·<code>인천 제물포구</code>. 사람이 읽고 고르라고 붙입니다.</td><td>화면에 같이 보여 주세요. <b>정체성 키가 아닙니다</b> — 붙는다고 id 가 갈리지 않고, 같다고 합쳐지지도 않습니다.</td></tr>
<tr><td><code>candidate_ids</code></td><td>이름만으로는 어느 것인지 못 정했을 때의 <b>후보 전체</b>.</td><td>KDB 는 <b>자동으로 하나를 고르지 않습니다.</b> 후보가 여럿이면 기사 문맥으로 여러분이 고르거나, <code>context</code> 를 더 주고 다시 물으세요.</td></tr>
</table>
<div class="note"><b>그래서 <code>context</code> 가 필수입니다.</b> "그룹 CLC 의 채영"과 "트와이스 채영"은
문맥이 없으면 가릴 수 없습니다. 문맥을 주면 KDB 가 고르고, 못 고르면 후보를 돌려줍니다 —
<b>틀린 하나를 골라 주지 않습니다.</b></div>
<div class="note"><b>기사 하나를 한 번에 보내면 정확도가 올라갑니다.</b> 같은 기사에 배우 A 와
드라마 B 가 같이 나온다는 사실이 동명이인 해소에 가장 값진 신호입니다. 하나씩 따로 물으면
그 신호가 사라집니다.</div>

<h2>3. 값이 없을 때 — 빈칸 계약</h2>
<p><b>KDB 는 DB 에 있으면 주고 없으면 주지 않습니다.</b> 추측으로 채워 보내지 않습니다
(실측: 기계번역으로 채웠더니 <code>수제천</code>→<code>手工制作的</code> 처럼 40%가 틀렸습니다).</p>
<p>대신 <b>왜 없는지</b>와 <b>어떻게 채워야 하는지</b>를 알려 줍니다. <code>include_absent:true</code> 로 부르세요 —
<b><code>/v1/entities/match</code>·<code>/v1/lookup</code>·<code>/v1/lookup/bulk</code> 모두 같습니다.</b>
<code>/v1/prepare</code> 는 <code>missing</code> 각 locale 에 <code>absent_locales</code> 로 같은 안내를 붙입니다.</p>
<pre>POST /v1/entities/match
{ <span class="k">"source_text"</span>:<span class="s">"…"</span>, <span class="k">"locale"</span>:<span class="s">"ja"</span>, <span class="k">"include_absent"</span>:true }
→ { <span class="k">"ko"</span>:<span class="s">"기쁜 우리 좋은 날"</span>, <span class="k">"locale_name"</span>:<span class="s">""</span>,
     <span class="k">"no_value"</span>:<span class="s">"no_value"</span>, <span class="k">"fill_hint"</span>:<span class="s">"translate_title"</span> }

<span class="c">// lookup / lookup/bulk 는 대상별로 묶어서 줍니다</span>
POST /v1/lookup/bulk
{ <span class="k">"queries"</span>:[{<span class="k">"ko"</span>:<span class="s">"기쁜 우리 좋은 날"</span>,<span class="k">"type"</span>:<span class="s">"drama"</span>}],
  <span class="k">"include_absent"</span>:true, <span class="k">"locales"</span>:[<span class="s">"ja"</span>,<span class="s">"es"</span>] }
→ <span class="k">"absent_locales"</span>: { <span class="k">"ja"</span>:{<span class="k">"locale_absent"</span>:<span class="s">"no_value"</span>,<span class="k">"fill_hint"</span>:<span class="s">"translate_title"</span>},
                       <span class="k">"es"</span>:{<span class="k">"locale_absent"</span>:<span class="s">"llm_only"</span>, <span class="k">"fill_hint"</span>:<span class="s">"translate_title"</span>} }</pre>
<table>
<tr><th>no_value</th><th>뜻</th><th>여러분이 할 일</th></tr>
<tr><td><code>no_value</code></td><td>그 locale 칸이 비어 있음</td><td><code>fill_hint</code> 대로 채우고, §4-1 로 되돌려 보내 주세요</td></tr>
<tr><td><code>fallback_en</code></td><td>영문으로 대체 중</td><td>영문을 써도 되는 자리면 그대로, 아니면 채워서 보내 주세요</td></tr>
<tr><td><code>llm_only</code></td><td>LLM 합성값만 있음</td><td>검증 안 된 값입니다. 자체 검증 후 사용</td></tr>
<tr><td><code>unverified_source</code></td><td>검증 안 된 출처의 값만 있음</td><td>같음</td></tr>
</table>
<table>
<tr><th>fill_hint</th><th>대상</th><th>어떻게</th></tr>
<tr><td><code>transliterate</code></td><td>사람·그룹·장소</td><td><b>음역</b>하세요. 번역하면 안 됩니다(박보검 → パク・ボゴム, "기쁜 박" 아님)</td></tr>
<tr><td><code>translate_title</code></td><td>작품 제목·행사명</td><td><b>번역</b>하세요. 음역하면 안 됩니다(오징어 게임 → Squid Game, "Ojingeo Game" 아님)</td></tr>
</table>
<div class="note">이 두 값을 지키는 것만으로 표기 품질이 크게 올라갑니다 —
KDB 자신도 이것을 어겨서 <b>작품 제목 6,640칸을 로마자로 채운</b> 적이 있습니다(2026-09 발견·수정).</div>

<h2>4. 협업 워크플로우</h2>
<p>단방향 제공이 아니라, 클라이언트와 교류하며 품질을 올립니다.</p>
<div class="flow">
<div class="step"><b>① 받기/준비</b><br>기사 작성 시점에 고유명사(한글)를 <code>/v1/prepare</code> 로 미리 던지면, 조회 전에 번역을 준비합니다.</div>
<div class="step"><b>② 보내기</b><br><code>/v1/lookup</code>·<code>/v1/entities/match</code> 로 완성된 다국어 표기를 조회합니다.</div>
<div class="step"><b>③ 개선</b><br>오역을 발견하면 <code>/v1/corrections</code> 로 신고 → 검증되면 자동 반영됩니다.</div>
</div>

<h2>5. 인증 · 한도 · 오류</h2>
<p>모든 <code>/v1/*</code> 요청은 API 키가 필요합니다(헤더). <code>/docs</code>·<code>/v1/health</code> 는 무인증.</p>
<pre><span class="k">X-KDB-Key</span>: &lt;your-api-key&gt;   <span class="c"># 또는 Authorization: Bearer &lt;key&gt;</span></pre>

<h3>5-1. 키 등급</h3>
<table>
<tr><th>등급</th><th>누구</th><th>쓸 수 있는 것</th></tr>
<tr><td><code>read</code></td><td>소비자(여러분)</td><td>조회 · <code>/v1/prepare</code> · <code>/v1/corrections</code> · <code>/v1/observations</code></td></tr>
<tr><td><code>write</code></td><td>운영자 전용</td><td>위 + 값 직접 수정 · 잠금 · 외부 사이트 검색</td></tr>
</table>
<div class="note">소비자 키로 쓰기 엔드포인트를 부르면 <code>403</code> 입니다. 정상입니다 —
소비자가 값을 직접 바꾸는 경로는 없습니다. 고칠 것이 있으면 <code>/v1/corrections</code> 로 신고하세요.</div>

<h3>5-2. 한도</h3>
<p>IP 당 <b>분당 120 요청</b>. 넘으면 <code>429</code> 와 <code>Retry-After: 60</code> 이 옵니다.
묶어 보낼 수 있는 엔드포인트(<code>/v1/lookup/bulk</code> · <code>/v1/entities/match/bulk</code> ·
<code>/v1/prepare</code> 의 terms 배열)를 쓰면 한도를 거의 안 씁니다 — <b>기사 하나를 한 번에</b> 보내세요.</p>

<h3>5-3. 오류 형식</h3>
<p>모든 오류는 같은 형태입니다. 빈 결과는 오류가 아니라 <b>빈 배열</b>입니다.</p>
<pre>{ <span class="k">"ok"</span>: false,
  <span class="k">"error"</span>: { <span class="k">"code"</span>: <span class="s">"bad_request"</span>, <span class="k">"message"</span>: <span class="s">"terms required"</span> } }</pre>
<table>
<tr><th>HTTP</th><th>code</th><th>언제</th><th>어떻게</th></tr>
<tr><td>400</td><td><code>bad_request</code></td><td>JSON 오류 · 필수 필드 누락 · 잘못된 id/locale</td><td>고쳐서 다시. 그대로 재시도하면 계속 400</td></tr>
<tr><td>401</td><td><code>unauthorized</code></td><td>키 없음·틀림</td><td>헤더 확인</td></tr>
<tr><td>403</td><td><code>forbidden</code></td><td>read 키로 write 엔드포인트</td><td>쓰지 마세요(3-1)</td></tr>
<tr><td>404</td><td><code>not_found</code></td><td>없는 id</td><td>재시도 무의미</td></tr>
<tr><td>429</td><td>—</td><td>분당 120 초과</td><td><code>Retry-After</code> 만큼 쉬고 재시도. 묶어 보내면 안 납니다</td></tr>
<tr><td>503</td><td><code>unavailable</code></td><td>일시적(DB 등)</td><td><b>재시도 가능.</b> 지수 백오프</td></tr>
<tr><td>500</td><td><code>internal</code></td><td>우리 쪽 결함</td><td>재시도 후에도 나면 알려 주세요</td></tr>
</table>

<h2>6. 엔드포인트</h2>

<h3>POST /v1/prepare — 받기 + 빠른 준비</h3>
<p>기사에 등장할 한글 고유명사를 미리 던지면 KDB 가 그 사이 번역을 준비합니다.
각 term 은 문자열 또는 <code>{ko,type,context}</code>. 신규 이름의 자동 조사는 이상 키워드 비용을 막기 위해
<b>type + source_url + 실제 언급 문맥(context) + 타입 단서</b>가 함께 확인될 때만 시작합니다.</p>
<pre>POST /v1/prepare
{ <span class="k">"terms"</span>: [
    {<span class="k">"ko"</span>:<span class="s">"박보검"</span>, <span class="k">"type"</span>:<span class="s">"person"</span>, <span class="k">"context"</span>:<span class="s">"배우 박보검이 출연했다"</span>},
    {<span class="k">"ko"</span>:<span class="s">"폭싹 속았수다"</span>, <span class="k">"type"</span>:<span class="s">"drama"</span>, <span class="k">"context"</span>:<span class="s">"드라마 '폭싹 속았수다'가 시청률 1위를 기록했다"</span>}
  ],
  <span class="k">"locales"</span>: [<span class="s">"ja"</span>,<span class="s">"zh"</span>,<span class="s">"es"</span>],   <span class="c">// 생략 시 9개 전체</span>
  <span class="k">"source_url"</span>: <span class="s">"https://kstory.aiinplanet.com/…"</span>,  <span class="c">// 필수: 출처 기사 (§7 요청 계약)</span>

  <span class="c">// 선택: 여러분이 이미 만들어 둔 표기를 같이 보냅니다(§6-1 제안 표기).</span>
  <span class="k">"suggestion_meta"</span>: {<span class="k">"producer"</span>:<span class="s">"presslocale"</span>, <span class="k">"model"</span>:<span class="s">"gpt-5.6-sol"</span>, <span class="k">"reasoning"</span>:<span class="s">"low"</span>}
}
<span class="c">// term 안에 넣습니다:  {"ko":"기쁜 우리 좋은 날", "type":"drama",
//   "suggestions": {"ja":{"value":"私たちのうれしい良き日","basis":"literal"}}}</span>
→ { <span class="k">"items"</span>: [
    { <span class="k">"term"</span>:<span class="s">"박보검"</span>, <span class="k">"status"</span>:<span class="s">"ready"</span>, <span class="k">"type"</span>:<span class="s">"person"</span>,
      <span class="k">"values"</span>:{<span class="k">"ja"</span>:<span class="s">"パク・ボゴム"</span>,<span class="k">"zh"</span>:<span class="s">"朴宝剑"</span>,<span class="k">"es"</span>:<span class="s">"Park Bo-gum"</span>} },
    { <span class="k">"term"</span>:<span class="s">"폭싹 속았수다"</span>, <span class="k">"status"</span>:<span class="s">"preparing"</span>, <span class="k">"missing"</span>:[<span class="s">"es"</span>] },
    { <span class="k">"term"</span>:<span class="s">"아이유"</span>, <span class="k">"status"</span>:<span class="s">"ready"</span>, ... } ] }</pre>
<table>
<tr><th>status</th><th>의미</th></tr>
<tr><td class="ok">ready</td><td>요청 locale 다 준비됨 — 즉시 사용 가능</td></tr>
<tr><td>preparing</td><td>빈 locale 을 백그라운드로 준비 중 — <b>채워질 수도, 끝내 안 채워질 수도 있습니다.</b> 실측(2026-09-15, 14일): 채워진 건의 중앙값 <b>44분</b>, 1시간 내 58%, 상위 10%는 7일 넘게 걸립니다. 최종적으로 채워지는 비율은 <b>약 60%</b>. 나머지는 아래 <code>unfillable</code>·<code>review</code> 로 갈려 통지됩니다</td></tr>
<tr><td>new</td><td>처음 보는 고유명사 — 발굴·분류 파이프라인 진입(K-콘텐츠면 준비)</td></tr>
<tr><td>preparing (신규어)</td><td>근거 부족한 처음 보는 키워드 — KDB 가 자동 검증(Naver 근거수집) 후 발굴 진행. context/type 을 함께 보내면 검증을 건너뛰고 즉시 발굴(new)</td></tr>
<tr><td class="warn">review</td><td>사람 판단이 필요한 항목 — <b>기다린다고 저절로 채워지지 않습니다.</b> 근거 URL 을 <code>/v1/corrections</code> 로 보내주시면 재심합니다</td></tr>
<tr><td class="warn">unfillable</td><td><b>대상은 확인했으나 현지 표기 근거를 찾지 못했습니다</b> — <b>재조회해도 채워지지 않습니다.</b> 근거 URL 을 <code>/v1/corrections</code> 로 보내주시면 재심합니다</td></tr>
<tr><td class="warn">out_of_scope</td><td><b>고유명사 입력 규칙에서 기각</b>되었거나 이미 검토가 끝나 '결번' 판정된 키워드 — 준비/등록하지 않습니다. <b>같은 요청은 같은 답</b>이므로 재조회는 도움이 되지 않습니다. <b>다만 아래 «판정 만료»를 보세요</b> — 우리 규칙이 바뀌거나 그 판정의 근거가 된 사실이 바뀌면 판정은 스스로 만료되고 다시 발굴합니다</td></tr>
</table>
<div class="note"><b>★판정 만료 — <code>out_of_scope</code>·<code>review</code> 가 영구 차단이 아닌 이유.</b>
기각·검토 판정에는 그때 적용한 <b>규칙 판본</b>이 함께 기록됩니다. 두 경우에 그 판정은 스스로 만료되고,
다음 요청은 <b>처음 보는 낱말처럼</b> 지금 규칙으로 다시 판단됩니다.
<ul>
<li><b>우리 규칙이 바뀌었을 때.</b> 2026-09-15 범위 확대(정치·경제·시사·스포츠)가 그런 경우였습니다 —
그전에 "K-엔터테인먼트가 아님"으로 기각된 판정은 <b>전부 만료</b>되었습니다.
이재명·차범근·더불어민주당·서울대학교가 여기 해당했습니다.</li>
<li><b>판정의 근거가 된 사실이 바뀌었을 때.</b> 예: "같은 이름의 기각 행이 있다"를 이유로 닫힌 요청은,
그 행이 되살아나면 근거가 사라지므로 만료됩니다.</li>
</ul>
<b>규칙 판본이 바뀌면 이 문서에 적습니다.</b> 그때 재조회하시면 됩니다 — 평소에는 재조회가 도움이 되지 않습니다.
<br>«표기 근거를 못 찾음»(<code>unfillable</code>)은 만료되지 않습니다. 규칙이 아니라 <b>관측</b>의 문제라,
우리 규칙이 바뀌어도 그 표기가 생기지는 않기 때문입니다 — 근거 URL 을 <code>/v1/corrections</code> 로 보내주세요.</div>

<div class="note warn"><b><code>preparing</code> 은 "잠시 후 다시" 이고, <code>unfillable</code>·<code>review</code>·<code>out_of_scope</code> 는 "다시 물어도 같다" 입니다.</b>
종전에는 끝난 것까지 <code>preparing</code> 으로 답해, 영영 오지 않을 답을 계속 물으시게 했습니다
(실측 2026-09-15: <code>preparing</code> 으로 답한 낱말의 절반 이상이 하루가 지나도 안 채워졌고,
발굴 큐에서는 이미 종결돼 있었습니다). 이제 종결된 것은 종결이라고 답하고,
<b><code>resolution</code></b> 필드에 이유와 할 일을 같이 보냅니다.</div>
<div class="note warn"><b>값이 어디서 왔는지는 묻지 않아도 알려드립니다 — <code>locale_provenance</code>.</b>
2026-09-15 부터 조회 응답의 모든 locale 값에 출처 라벨이 함께 옵니다.
<b>지금 서빙되는 영문 표기의 33%(4,204칸)는 기계번역(<code>machine-translation</code>)입니다</b>
— 일본어는 18.9%. 우리가 "추측으로 채워 보내지 않는다"고 적어 둔 약속은
<code>verified_only:true</code> 를 쓸 때만 지켜지는데, 그걸 쓰지 않으면 검증된 값과
구분 없이 섞여 나갔고 <b>구분할 방법이 없었습니다</b>.
검증된 출처는 <code>operator-locked</code>·<code>wikidata-label</code>·<code>external-db</code>·<code>media-consensus</code> 넷입니다.
그 밖의 라벨이 붙은 값은 <b>그대로 발행하지 마시고</b> 자체 검수하거나
<code>verified_only:true</code> 로 부르세요.</div>

<p><b>unavailable</b>(선택 필드): missing 중 현재 보유 소스가 모두 소진돼 채울 수 없는 locale.
값을 추측으로 채우지 않는다는 원칙(빈칸&gt;틀린값)의 종결 통지 — 해당 locale 은 재폴링해도
바뀌지 않습니다(새 소스 확보 시 자동 재개). lookup 응답에도 <b>status</b>
(found | miss | out_of_scope) 가 함께 옵니다.</p>

<h3>6-1. 제안 표기 — 여러분이 만든 값을 같이 보내기</h3>
<p>사이트마다 같은 이름을 따로 번역하면 표기가 갈립니다(실측: 한 제목의 일본어 직역이
8회 호출에 2~4가지). 그래서 <b>여러분이 이미 만든 표기를 <code>/v1/prepare</code> 에 같이 실어
보낼 수 있습니다.</b> 먼저 보낸 값이 남고, 다른 사이트도 같은 값을 가져다 쓸 수 있습니다.</p>
<table>
<tr><th>필드</th><th>설명</th></tr>
<tr><td>term.suggestions</td><td><code>{ "&lt;locale&gt;": {"value":"…","basis":"literal|transliteration|official"} }</code></td></tr>
<tr><td>suggestion_meta.producer</td><td><b>필수.</b> 누가 만들었나(<code>presslocale</code>). 없으면 받지 않습니다 — 출처 없는 제안은 재료도 아닙니다.</td></tr>
<tr><td>suggestion_meta.model / reasoning</td><td>어떤 모델·어느 강도로 만들었나. 받는 쪽이 신뢰도를 스스로 판단하도록 그대로 보관·전달합니다.</td></tr>
</table>
<div class="note"><b>제안은 값이 아니라 재료입니다.</b> KDB 의 표기 칸에 들어가지 않고,
<code>verified_only</code> 조회에 절대 섞이지 않으며, <b>승격 경로가 없습니다</b>.
제안이 먼저 있었다는 사실은 검증의 근거가 되지 못합니다 — 그렇게 하면 여러분이 보낸 값이
"검증된 값"으로 되돌아옵니다(순환 오염).</div>
<div class="note"><b>입증되면 교체됩니다.</b> KDB 가 권위 출처(TMDb·KOFIC·MusicBrainz·운영자 등)로
그 locale 값을 확정하면 제안은 그 즉시 물러나고 조회에서 빠집니다. 기록은 남습니다 —
우리 값과 여러분 제안이 얼마나 맞았는지가 제안 품질의 유일한 지표입니다.</div>
<div class="note">이름·locale·제작처당 <b>하나만</b> 유지합니다. 호출마다 달라지는 직역을 매번 보내면
후보가 여러 개 쌓여 "먼저 정한 것을 모두가 다시 쓴다"는 목적 자체가 깨집니다.
같은 값이 다시 오면 횟수만 올라갑니다. 그러니 <b>사이트 용어집에 고정한 값만</b> 보내십시오.</div>
<div class="note">제안값은 <b>멱등성 지문에 들어가지 않습니다.</b> 같은 기사를 다시 준비할 때
모델 출력이 달라져도 같은 <code>Idempotency-Key</code> 로 같은 준비 건에 이어집니다.</div>

<h3>POST /v1/entities/match — 본문에서 매칭(번역 핫패스)</h3>
<p>기사 본문에서 알려진 엔티티를 찾아 목표 locale 표기로 매핑합니다.</p>
<pre>POST /v1/entities/match
{ <span class="k">"source_text"</span>:<span class="s">"방탄소년단의 새 앨범…"</span>, <span class="k">"locale"</span>:<span class="s">"ja"</span>,
  <span class="k">"min_confidence"</span>:0.9, <span class="k">"verified_only"</span>:true,
  <span class="k">"disambiguate"</span>:true }   <span class="c">// 선택: 기사 맥락으로 오매칭·일반어·동명이의 제거</span></pre>
<table>
<tr><th>응답 필드</th><th>설명</th></tr>
<tr><td>locale_name</td><td>해당 locale 표기/번역</td></tr>
<tr><td>provenance</td><td>반환값의 출처 등급: <code>operator-locked</code> · <code>wikidata-label</code> · <code>external-db</code> · <code>media-consensus</code> · <code>wikipedia-langlinks</code> · <code>media-single</code> · <code>llm-only</code></td></tr>
<tr><td>locale_source</td><td>그 값의 raw source(소비자 자체 게이팅용)</td></tr>
<tr><td>id</td><td>대상의 UUID. <b>검색 키가 아니라 신원 표식입니다</b> — 표기가 같아도 id 가 다르면 다른 대상이고, id 가 같으면 같은 대상입니다(채영(TWICE) ≠ 채영(CLC)).</td></tr>
<tr><td>disambig</td><td>같은 이름을 가리기 위한 표시 한정어(<code>(가수)</code> · <code>인천 제물포구</code>). <b>정체성 키가 아닙니다</b> — 붙는다고 id 가 갈리지 않습니다.</td></tr>
<tr><td>no_value / locale_absent</td><td><code>include_absent:true</code> 일 때, 값이 없는 이유: <code>no_value</code>(빈칸) · <code>fallback_en</code>(영문으로 대체) · <code>llm_only</code>(LLM 합성뿐) · <code>unverified_source</code>(검증 안 된 출처뿐)</td></tr>
<tr><td>fill_hint</td><td>그 빈칸을 <b>어떻게</b> 채워야 하는지: <code>transliterate</code>(사람·지명 — 음역) · <code>translate_title</code>(작품 제목 — 번역). 빈칸을 받았을 때 여러분이 무엇을 해야 하는지 알려 주는 필드입니다.</td></tr>
</table>
<div class="note"><b>빈칸을 받았을 때.</b> KDB 는 <b>DB 에 있으면 주고 없으면 주지 않습니다</b> —
추측으로 채워 보내지 않습니다. 대신 <code>include_absent:true</code> 로 부르면 <b>왜 없는지</b>와
<b>어떻게 채워야 하는지</b>를 함께 돌려줍니다. 그 값으로 여러분이 채운 뒤
§6-1 제안 표기로 되돌려 보내 주시면, 다음 요청부터 다른 사이트도 같은 값을 씁니다.</div>
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

<h3>6-2. 준비 건 폴링 — <code>preparing</code> 을 받았을 때</h3>
<p><code>/v1/prepare</code> 는 준비 건 하나를 만듭니다. <code>preparing</code> 이 왔다면 같은 요청을
다시 보내지 말고 <b>그 건을 조회</b>하세요. 재전송은 중복 종결됩니다.</p>
<pre>POST /v1/prepare     <span class="c"># Idempotency-Key: &lt;기사ID+버전&gt;  ← 같은 키면 같은 건에 이어집니다</span>
→ { <span class="k">"preparation_id"</span>:<span class="s">"…"</span>, <span class="k">"items"</span>:[…] }

GET /v1/preparations/{id}
→ { <span class="k">"status"</span>:<span class="s">"ready"</span>, <span class="k">"items"</span>:[ { <span class="k">"term"</span>:…, <span class="k">"resolved_entity_id"</span>:…,
      <span class="k">"candidate_ids"</span>:[…], <span class="k">"identity_state"</span>:<span class="s">"resolved"</span>,
      <span class="k">"locales"</span>:[ {<span class="k">"locale"</span>:<span class="s">"ja"</span>,<span class="k">"state"</span>:<span class="s">"ready"</span>,<span class="k">"value"</span>:…,<span class="k">"source"</span>:…} ] } ] }

POST /v1/preparations/{id}/cancel   <span class="c"># 기사가 엎어졌을 때</span></pre>
<table>
<tr><th>locale.state</th><th>뜻</th><th>다시 물어야 하나</th></tr>
<tr><td class="ok">ready</td><td>쓸 수 있는 값이 있음</td><td>아니오</td></tr>
<tr><td>pending</td><td>준비 중</td><td>예 — 중앙값 44분 뒤(실측). 몇 초 단위 재조회는 한도만 씁니다</td></tr>
<tr><td>no_evidence</td><td>근거를 못 찾음</td><td>아니오 — 새 출처가 생기면 자동 재개</td></tr>
<tr><td>no_form</td><td>그 locale 에 쓸 표기 형태가 없음</td><td>아니오</td></tr>
<tr><td class="warn">unavailable</td><td>보유 소스가 모두 소진됨</td><td><b>아니오 — 재폴링해도 안 바뀝니다</b></td></tr>
</table>
<div class="note"><b>폴링 예산.</b> <code>preparing</code> 이면 15초 뒤 한 번, 그 뒤 60초 간격으로
최대 5회면 충분합니다. 그래도 <code>pending</code> 이면 그 locale 은 지금 못 채우는 것이니
§3 빈칸 계약대로 처리하세요. 무한 폴링은 분당 120 한도만 씁니다.</div>

<h3>6-3. 조회 엔드포인트</h3>
<table>
<tr><th>엔드포인트</th><th>쓰임</th><th>비고</th></tr>
<tr><td><code>POST /v1/lookup</code></td><td>한글명 단건 검색</td><td>miss 면 최우선 발굴 레인 진입. <code>verified_only</code> · <code>include_absent</code>+<code>locales</code> 를 받습니다(§3)</td></tr>
<tr><td><code>POST /v1/lookup/bulk</code></td><td>여러 이름 한 번에 (최대 50). <b>이름마다 <code>type</code>·<code>context</code></b> 를 붙일 수 있습니다</td><td><b>권장</b> — 한도를 아낍니다. <code>verified_only</code>·<code>include_absent</code>·<code>status</code> 는 단건과 동일</td></tr>
<tr><td><code>POST /v1/entities/match</code></td><td>기사 본문에서 찾아 매핑</td><td>번역 핫패스</td></tr>
<tr><td><code>POST /v1/entities/match/bulk</code></td><td>여러 본문 한 번에</td><td><b>권장</b></td></tr>
<tr><td><code>GET /v1/entities?q=&amp;type=&amp;updated_since=</code></td><td>목록 · <b>델타 동기화</b></td><td>주기적으로 개선분만 받기</td></tr>
<tr><td><code>GET /v1/entities/{id}</code></td><td>단건 상세</td><td><b>id 를 저장해 뒀다면 이걸 쓰세요</b> — 동명이인 걱정이 없습니다</td></tr>
<tr><td><code>GET /v1/entities/{id}/spellings</code></td><td>그 대상의 locale 별 표기 전체 + 출처</td><td>어느 값이 어디서 왔는지 볼 때</td></tr>
<tr><td><code>GET /v1/entities/{id}/external-refs</code></td><td>외부 식별자(Wikidata·TMDb·MusicBrainz…)</td><td>여러분 DB 와 이어 붙일 때</td></tr>
<tr><td><code>GET /v1/entities/{id}/relations</code></td><td>관계(소속·출연 등)</td><td></td></tr>
<tr><td><code>GET /v1/persons/{id}</code></td><td>인물 상세(소속사·대표작·생년)</td><td></td></tr>
<tr><td><code>GET /v1/kentity/entities?q=</code></td><td>통합 목록 검색 — <b>구분값(qualifier) 포함</b></td><td>정확일치가 먼저 옵니다</td></tr>
<tr><td><code>GET /v1/kentity/entities/{id}</code></td><td>통합 단건 — 이름·근거 전체</td><td></td></tr>
<tr><td><code>GET /v1/health</code></td><td>상태 · 보유 규모(무인증)</td><td>고정 숫자를 문서에 쓰지 마세요</td></tr>
</table>

<h3>6-3-1. 조회 응답의 <code>status</code></h3>
<table>
<tr><th>status</th><th>뜻</th><th>할 일</th></tr>
<tr><td class="ok">found</td><td>대상 하나를 찾음</td><td>쓰세요</td></tr>
<tr><td class="warn">ambiguous</td><td><b>같은 이름의 다른 대상이 둘 이상</b></td><td><b>KDB 는 하나를 골라 주지 않습니다.</b> <code>disambig</code>·<code>agency</code>·<code>primary_role</code>·<code>birth_year</code>·<code>notable_works</code> 를 보고 기사 문맥으로 고르세요. 못 고르겠으면 <code>context</code> 를 넣어 다시 물으세요</td></tr>
<tr><td>miss</td><td>없음 — 발굴 큐에 넣음</td><td><code>/v1/prepare</code> 로 유형·문맥·URL 과 함께 보내세요</td></tr>
<tr><td class="warn">out_of_scope</td><td>검토가 끝나 범위 밖으로 판정됨</td><td><b>재조회 불필요</b></td></tr>
</table>
<div class="note"><b>왜 안 골라 주나.</b> 문맥 없이 KDB 가 하나를 고르면 <b>틀린 사람을 확정</b>하고,
여러분이 그것을 id 로 저장합니다. 그 오류는 기사마다 따라다닙니다.
그래서 <b>이름마다 <code>type</code> 을, 애매하면 <code>context</code> 를</b> 같이 보내 주세요 —
그 재료가 있으면 KDB 가 고를 수 있습니다.</div>

<h3>6-4. POST /v1/observations — 매체가 실제로 쓴 표기 알려 주기</h3>
<p>여러분 사이트나 다른 현지 매체가 <b>실제로 사용한</b> 표기를 봤다면 알려 주세요.
독립된 매체 여러 곳이 같은 표기를 쓰면 KDB 가 그것을 <b>검증값으로 승격</b>합니다(현지 합의).</p>
<pre>POST /v1/observations
{ <span class="k">"entity_id"</span>:<span class="s">"84ab95a9-…"</span>, <span class="k">"locale"</span>:<span class="s">"ja"</span>,
  <span class="k">"spelling"</span>:<span class="s">"チェヨン"</span>, <span class="k">"source_domain"</span>:<span class="s">"natalie.mu"</span>,
  <span class="k">"source_url"</span>:<span class="s">"https://natalie.mu/…"</span> }</pre>
<div class="note warn"><b>§6-1 제안 표기와 다릅니다.</b> 관측은 "<b>누가 실제로 이렇게 썼다</b>"는
목격이고, 제안은 "<b>우리 모델이 이렇게 만들었다</b>"입니다. 모델이 만든 값을 관측으로 보내면
지어낸 것을 목격으로 기록하는 것이 됩니다 — 그것만은 하지 마세요. 관측은 승격 경로가 있고
제안은 없습니다.</div>

<h2>7. 요청 계약 (Request Contract) — 필수 payload 규칙</h2>
<p><b>2026-07-16 제정.</b> KDB 의 처리 속도·정확도는 요청 품질이 결정합니다. 아래는 권장이 아니라
<b>계약</b>입니다 — 준수율은 소비자별로 측정·공개되며, 무타입·무맥락 키워드는 심사 보류(저품질
레인)로 빠져 <b>처리가 수 시간 지연</b>됩니다. 잘못된 type 은 DB 를 오염시킵니다.</p>

<h3>7-1. 필수 필드 (term 은 반드시 객체로)</h3>
<table>
<tr><th>필드</th><th>필수</th><th>규칙</th></tr>
<tr><td><code>terms[].ko</code></td><td class="ok">필수</td><td>고유명사 <b>원형 1개만</b>. 기사 표기 그대로. 조합어·수식어·병기 금지(7-3)</td></tr>
<tr><td><code>terms[].type</code></td><td class="ok">필수</td><td>7-2 판별표로 기사에서 판별해 지정. 판별 불가 시에만 <code>unknown</code></td></tr>
<tr><td><code>terms[].context</code> (또는 batch <code>context</code>)</td><td class="ok">필수</td><td>키워드가 등장한 <b>기사 문장 1개</b>(±200자). 동명이인 구분의 핵심 재료</td></tr>
<tr><td><code>source_url</code> (batch, term별 override 가능)</td><td class="ok">필수</td><td>키워드가 나온 기사 URL. 신뢰 매체면 심사 통과 가속</td></tr>
</table>
<div class="note">긴급도는 필드가 아니라 <b>엔드포인트</b>로 구분됩니다: 지금 번역에 필요하면
<code>/v1/lookup</code>(miss 시 최우선 발굴), 곧 쓸 키워드는 <code>/v1/prepare</code>(사전 준비 레인).</div>

<h3>7-2. 타입 판별표 — 기사 단서 → type (시스템이 기계적으로 판별하도록 구현)</h3>
<table>
<tr><th>기사 단서</th><th>type</th></tr>
<tr><td>"배우/가수/래퍼/개그맨/PD ○○", "그룹 △△의 멤버 ○○" (실존 인물)</td><td><code>person</code></td></tr>
<tr><td>"그룹/밴드/듀오/팀 ○○"</td><td><code>group</code></td></tr>
<tr><td>"드라마 ○○", 방영·회차·시청률 문맥</td><td><code>drama</code></td></tr>
<tr><td>"영화 ○○", 개봉·관객수 문맥</td><td><code>movie</code></td></tr>
<tr><td>"예능/프로그램/방송 ○○"</td><td><code>show</code></td></tr>
<tr><td>"곡/신곡/타이틀곡/앨범 ○○", 발매·수록 문맥</td><td><code>song_album</code></td></tr>
<tr><td>"시상식/콘서트/투어/페스티벌 ○○"</td><td><code>event_tour</code></td></tr>
<tr><td>"소속사/기획사/제작사 ○○"</td><td><code>agency</code></td></tr>
<tr><td>"채널/매체/플랫폼 ○○"</td><td><code>channel_outlet</code></td></tr>
<tr><td>상표·장소·시설</td><td><code>brand_place</code></td></tr>
<tr><td>드라마 속 <b>배역 이름</b>(실존 인물 아님)</td><td><code>character</code></td></tr>
</table>
<div class="note">★혼동 주의: <b>사람 이름은 그의 곡·작품이 아니라 person</b> 입니다. 실존 인물은
character 가 아닙니다. "가수 박학기의 신곡 '바람이 분다'" → 박학기=<code>person</code>,
바람이 분다=<code>song_album</code> 로 <b>두 건</b>을 보냅니다.</div>

<h3>7-3. 보내면 안 되는 것 (서버가 기각·차단)</h3>
<table>
<tr><th>유형</th><th>예</th><th>서버 처리</th></tr>
<tr><td>조합어·수식어</td><td>아이유 콘서트 티켓 · 배우 아이유</td><td>보류/기각 — <code>아이유</code> 만 보내세요</td></tr>
<tr><td>일반명사·카테고리·장르어</td><td>배우, 아이돌, 컴백, K-POP</td><td><code>category_not_entity</code> 기각</td></tr>
<tr><td>광고·상거래 키워드</td><td>○○광고, ○○예매, ○○할인, ○○다시보기</td><td><code>commodity_term</code> 즉시 기각</td></tr>
<tr><td><b>유형 없이</b> 던진 로마자</td><td>HIGH TOP, XYZ, R.I.P (수록곡 리스트를 통째로)</td><td><code>latin_passthrough</code> 자동 종결. <b>type 을 붙이면 곡·앨범도 정상 처리됩니다</b>(2026-09-15 변경). 종전에는 <code>song_album</code> 이면 유형을 붙여도 막았는데, 재 보니 로마자 제목의 11%가 일본어·중국어에서 자기 문자로 쓰입니다(New Woman → ニュー・ウーマン·新女性). 라틴 문자권(en·es·vi)만 원문 그대로입니다</td></tr>
<tr><td>기사 명사 전체 투척</td><td>기사에서 추출한 모든 명사 목록</td><td>보류 적체 — 번역에 실제 필요한 고유명사만</td></tr>
<tr><td>같은 키워드 수 분 내 반복</td><td>preparing 응답 직후 재전송</td><td>중복 종결 — 재전송이 아니라 <b>재조회</b>가 정답. 다만 실측 중앙값이 44분이니 <b>몇 초 뒤 재조회는 한도만 씁니다</b>. 종결 상태(<code>unfillable</code>·<code>review</code>·<code>out_of_scope</code>)를 받으면 다시 묻지 마세요</td></tr>
<tr><td><b>한국 대상이 아닌 인물·작품·서비스</b></td><td>크리스토퍼 놀런 · 로키 · 오모이노타케 · 그록</td><td>등록 안 됨 — 보류 큐 최장 21일 점유(§1 범위 참조). 보내기 전에 <b>"한국 대상인가"</b>를 확인(2026-09-15 기준 변경 — 분야는 묻지 않습니다)</td></tr>
<tr><td><b>오탈자·한영 혼종 문자열</b></td><td>JYP엔터테인<b>ement</b> (실측)</td><td>보류 — 전송 전 문자열 검증 필수. 올바른 원형: <code>JYP엔터테인먼트</code></td></tr>
<tr><td><b>이름+직함/수식 결합</b></td><td>박세영 감독 · 배우 아이유</td><td>보류/기각 — <b>이름만</b> 보내고 직함은 <code>context</code> 에 담으세요: <code>{"ko":"박세영","type":"person","context":"박세영 감독이 연출을 맡았다"}</code></td></tr>
</table>

<h3>7-4. source_url 규칙 — 실측 위반 기반 (2026-07-17 보강)</h3>
<p><code>source_url</code> 은 심사 게이트가 <b>키워드의 실재 근거</b>로 쓰는 값입니다.
아래 규칙을 어기면 근거로 인정되지 않아 심사가 느려집니다.</p>
<table>
<tr><th>규칙</th><th>실측 위반 사례</th><th>왜 문제인가</th></tr>
<tr><td><b>로그인 없이 열리는 공개 URL 만</b></td><td>비공개(회원전용/초안) 글 URL 이 그대로 전송됨 — 접속 시 <code>/login</code> 리다이렉트</td><td>KDB·운영자가 기사를 확인할 수 없어 근거 불인정. <b>발행되어 공개된 뒤의 URL</b> 을 보내세요. 발행 전에 미리 준비하려면 키워드는 먼저 던지되, 발행 직후 같은 terms 를 공개 URL 로 한 번 더 보내면 됩니다</td></tr>
<tr><td><b>canonical 경로 하나만</b></td><td>같은 글이 <code>/articles/{id}</code> 와 <code>/k-movie/{id}</code> 두 경로로 중복 전송</td><td>중복 심사 비용 + 어느 쪽이 진짜인지 불명. 사이트의 정식(canonical) 경로 하나로 통일</td></tr>
<tr><td><b>키워드가 실린 "그 기사" URL</b></td><td><code>en.wikipedia.org</code> 등 외부 참고 링크를 source_url 로 전송</td><td>source_url 은 참고자료가 아니라 <b>요청 출처 기사</b>입니다. 위키·검색결과·타사 링크는 근거로 인정 안 됨</td></tr>
</table>

<h3>7-5. 전송 전 자가 점검 체크리스트 (시스템에 그대로 구현하세요)</h3>
<ol>
<li>이 키워드는 <b>한국 대상</b>인가? (해외 인물·작품·서비스 → 보내지 않음. <b>분야는 묻지 않습니다</b> — 정치·경제·스포츠·학계도 범위 안)</li>
<li><code>ko</code> 는 <b>고유명사 원형 1개</b>인가? (직함·수식어·병기·문장 제거, 오탈자·한영 혼종 검증)</li>
<li><code>type</code> 을 7-2 판별표로 지정했는가? (<code>unknown</code> 은 최후수단 — 처리 최저속 레인)</li>
<li><code>context</code> 에 키워드가 등장한 <b>기사 문장 1개</b>를 담았는가? (키워드 반복·본문 통짜 금지)</li>
<li><code>source_url</code> 은 <b>로그인 없이 열리는, 키워드가 실린 그 기사의 canonical URL</b> 인가?</li>
<li>같은 키워드를 방금 보냈다면 재전송 대신 <b>재조회</b>하고 있는가? (preparing 은 실측 중앙값 44분 — 몇 초 뒤 재조회는 한도만 씁니다)</li>
</ol>
<div class="note"><b>준수가 곧 속도입니다.</b> 위 6항을 모두 갖춘 요청은 즉시심사(fresh 레인)로
수 초~수 분 내 처리되고, 빠진 요청은 보류 큐(수 시간~일 단위)로 빠집니다. 준수율은 소비자별로
상시 측정되어 admin 성적표에 공개됩니다.</div>

<h2>8. 붙이는 순서 (Integration)</h2>
<p>처음 붙이는 분은 이 순서대로 하면 됩니다.</p>

<h3>8-0. 기사 본문·제목은 보내지 마세요</h3>
<p>여러분이 기사에서 고유명사를 이미 뽑았다면 <b><code>/v1/entities/match</code> 는 쓸 필요가 없습니다.</b>
그 API 는 <b>본문을 받아 그 안에서 이름을 찾는</b> 용도입니다 — 이미 뽑아 뒀다면 본문을 보낼 이유가 없습니다.</p>
<div class="note"><b>기사에는 id 가 없습니다.</b> id 는 KDB 가 돌려주는 값이고,
<b>여러분 DB 에 이름→id 로 저장해 두는 것</b>입니다. 그러니 <b>언제나 이름으로 시작</b>합니다.
id 경로는 "전에 한 번 정해 둔 이름"에만 쓰는 지름길입니다.</div>
<table>
<tr><th>단계</th><th>KDB 로 가는 것</th></tr>
<tr><td>여러분이 기사를 읽고 고유명사·유형·제안 표기를 만든다</td><td><b>없음</b></td></tr>
<tr><td><b>이름으로 묻는다</b> — <code>POST /v1/lookup/bulk</code> (최대 50)</td><td><b>이름만</b></td></tr>
<tr><td>없는(miss) 것만 <code>POST /v1/prepare</code></td><td>이름 · 유형 · <b>본문 한 문장</b> · URL · 제안값</td></tr>
<tr><td>돌아온 <code>id</code> 를 여러분 DB 에 저장 → <b>다음 기사에서 같은 이름이 나오면</b> <code>GET /v1/entities/{id}</code></td><td><b>id 만</b>(2회차부터)</td></tr>
</table>
<div class="note"><b>기사 제목은 어느 단계에도 안 들어갑니다.</b> 제목을 <code>context</code> 로 보내면
유형 단서(<code>"배우 ○○"</code>)가 없어 심사가 보류로 빠집니다 — 제목이 아니라
<b>본문에서 그 이름이 나온 문장</b>을 보내세요.</div>
<div class="note"><b><code>context</code> 는 처음 보는 이름에만 필요합니다.</b> 이미 KDB 에 있는 이름은
문맥 없이도 그냥 나옵니다. 그러니 <b>① 이름만으로 먼저 묻고 ② miss 난 것만 문맥과 함께</b>
보내면, 본문이 나가는 것은 새 이름 몇 개 · 한 문장씩뿐입니다.</div>
<div class="note"><b>대상 id 를 저장하면 그 다음부터는 이름도 안 보냅니다.</b> 동명이인을 매번 다시
가릴 필요가 없어지고(§2), 요청이 가벼워져 한도(분당 120)를 거의 안 씁니다.</div>

<h3>8-1. 기사 하나를 처리하는 전체 흐름</h3>
<pre><span class="c">// ① 이름으로 묻는다. 기사에는 id 가 없으므로 언제나 여기서 시작합니다.</span>
<span class="c">//   (여러분 DB 에 이미 저장해 둔 이름은 건너뛰고 GET /v1/entities/{id} 로 바로)</span>
<span class="c">//   verified_only 로 출처 등급까지 같이 받습니다</span>
POST /v1/lookup/bulk
{ "queries": [                                   <span class="c">// 문자열도 되지만 객체로 보내세요</span>
    {"ko":"박보검","type":"person"},
    {"ko":"폭싹 속았수다","type":"drama"},
    {"ko":"채영","type":"person","context":"트와이스 채영이 …"} ],
  "verified_only": true }
→ { "results": [
     { "query":"박보검", "status":"found",
       "matches":[ { "id":"…", "canonical_ja":"パク・ボゴム",
                     "locale_provenance":{"ja":"wikidata-label"},
                     "verification_tier":"authoritative", "disambig":"" } ] },

     { "query":"채영", "status":"ambiguous",     <span class="c">// ★같은 이름의 다른 대상이 둘 이상</span>
       "matches":[ {"id":"84ab95a9…","disambig":"(TWICE)","agency":"JYP","canonical_ja":"チェヨン"},
                   {"id":"2dd14daf…","disambig":"(CLC)",  "canonical_ja":"チェヨン"} ] },

     { "query":"기쁜 우리 좋은 날", "status":"miss", "matches":[] } ] }

<span class="c">// ② miss 난 것만 준비 요청. 여기서만 본문 한 문장이 나갑니다</span>
POST /v1/prepare
{ "terms": [
    { "ko":"기쁜 우리 좋은 날", "type":"drama",
      "context":"드라마 '기쁜 우리 좋은 날'이 첫 방송됐다",   <span class="c">// 본문 문장. 제목 아님</span>
      "suggestions": { "ja":{"value":"私たちのうれしい良き日","basis":"literal"} } } ],
  "locales": ["ja","en","es"],
  "source_url": "https://…/article/70231",
  "article_id": "70231", "article_version": "v1",
  "verified_only": true,
  "suggestion_meta": {"producer":"presslocale","model":"gpt-5.6-sol","reasoning":"low"} }

<span class="c">// ③ 돌아온 id 를 여러분 DB 에 이름→id 로 저장한다</span>
<span class="c">//   → 다음 기사에서 같은 이름이 나오면 이름 조회를 건너뛸 수 있습니다</span>
<span class="c">// ④ 틀린 값을 보면 근거와 함께 /v1/corrections</span>
<span class="c">// ⑤ 하루 한 번 개선분만 받아 캐시 갱신</span>
GET /v1/entities?updated_since=2026-09-15T00:00:00Z</pre>

<h3>8-2. 여러분 쪽에 꼭 두어야 할 것</h3>
<table>
<tr><th>무엇</th><th>왜</th></tr>
<tr><td><b>이름 → id 표</b></td><td>기사에는 id 가 없습니다. 한 번 정해진 대상을 저장해 두면 다음부터 이름 조회를 건너뛰고, 동명이인을 다시 가릴 필요가 없습니다(§2)</td></tr>
<tr><td><b>사이트 용어집</b></td><td>한 번 고정한 표기를 기사마다 다시 만들지 않게. 여기 고정한 값만 <code>suggestions</code> 로 보내세요</td></tr>
<tr><td><b>폴링 예산</b></td><td>15초 → 60초 × 5회. 그 뒤엔 빈칸 계약대로(§3). 무한 폴링 금지</td></tr>
<tr><td><b>fill_hint 분기</b></td><td>음역할지 번역할지 KDB 가 알려 줍니다. 이것만 지켜도 품질이 크게 오릅니다</td></tr>
<tr><td><b>Idempotency-Key</b></td><td>기사ID+버전. 재시도·중복 전송이 같은 건에 모입니다</td></tr>
</table>

<h3>8-3. 자주 하는 실수</h3>
<table>
<tr><th>실수</th><th>결과</th><th>대신</th></tr>
<tr><td>기사 본문·제목을 KDB 로 전송</td><td>보낼 이유가 없는 데이터가 나감. 제목을 context 로 쓰면 유형 단서가 없어 보류</td><td>이름만 <code>lookup/bulk</code>. 새 이름만 본문 한 문장(§8-0)</td></tr>
<tr><td>고유명사를 하나씩 따로 요청</td><td>같은 기사라는 문맥이 사라져 동명이인을 못 가림</td><td>기사 하나 = 요청 하나 (terms 배열)</td></tr>
<tr><td>빈칸이 왔다고 계속 재요청</td><td>한도만 소모. 값은 안 바뀜</td><td><code>include_absent</code> 로 이유를 받고 §3 대로 처리</td></tr>
<tr><td>모델이 만든 값을 <code>observations</code> 로 전송</td><td>지어낸 것이 목격으로 기록됨</td><td><code>suggestions</code> 로 보내세요(§6-1 vs §6-4)</td></tr>
<tr><td><code>disambig</code> 로 대상을 식별</td><td>구분값은 정체성 키가 아님 — 같아도 다른 대상일 수 있음</td><td><code>id</code> 를 쓰세요</td></tr>
<tr><td>제목을 음역</td><td>포르투갈어에 <code>Gippeun Uri Joheun Nal</code> 같은 값이 나감</td><td><code>fill_hint: translate_title</code> 을 보고 번역</td></tr>
<tr><td>사람 이름을 번역</td><td>뜻으로 옮겨져 아무도 못 알아봄</td><td><code>fill_hint: transliterate</code> 을 보고 음역</td></tr>
</table>

<h2>9. 변경 이력</h2>
<p class="sub">규칙 판본이 붙은 항목은 <b>옛 판정을 만료시킵니다</b> — 아래 §10 을 보세요.
같은 내용이 <code>GET /v1/changelog</code> 로도 나갑니다(기계용).</p>
<table>
<tr><th>날짜</th><th>바뀐 것</th></tr>
<!--RULE-CHANGES-->
<tr><td>2026-09-15<br><span class="sub">(오후)</span></td><td>
<b>준비 상태가 정직해졌습니다.</b> 끝난 것까지 <code>preparing</code> 으로 답해 영영 오지 않을 답을
계속 물으시게 했습니다. 실측으로 <code>preparing</code> 이라 답한 낱말 188건 중 165건이 내부적으로는
이미 종결(표기 못 찾음 86 · 입력 규칙에서 막힘 79)이었습니다. 이제 종결은 종결이라고 답합니다 —
<code>unfillable</code>·<code>review</code> 를 추가하고 <code>resolution</code> 에 이유와 할 일을 함께 보냅니다.<br>
<b><code>preparing</code> 의 실제 소요를 실측으로 바꿨습니다.</b> 문서에 있던 "평균 15초"는 틀린 값이었습니다 —
실측 중앙값 <b>44분</b>, 1시간 내 58%, 최종 충족률 약 60%. 몇 초 뒤 재조회는 한도만 씁니다.<br>
<b>KDB 가 기사를 읽어 고유명사를 뽑아내지 않습니다(§1).</b> 지목해서 보내 주신 것만 받습니다.
종전에는 못 찾은 이름의 표기를 웹에서 찾아 보충했는데, 그 경로로 요청하지 않은 페이지의 낱말까지
원장에 들어오는 일이 있어 닫았습니다. 고유명사 분리는 보내는 쪽에서 하고,
<code>/v1/lookup/bulk</code> 의 <code>queries</code> 에 이름과 유형을 붙여 보내 주세요.
</td></tr>
<tr><td>2026-09-15</td><td><b>§6-1 제안 표기</b> 신설 — <code>suggestions</code>·<code>suggestion_meta</code> 를 받습니다(멱등성 지문에는 안 들어갑니다).<br>
<b>§3 빈칸 계약</b> 문서화 — <code>include_absent</code>·<code>no_value</code>·<code>fill_hint</code> 는 이전부터 나가고 있었으나 문서에 없었습니다.<br>
<b>§2 신원 모델</b> 문서화 — <code>id</code>·<code>disambig</code>·<code>candidate_ids</code>.<br>
<code>/v1/kentity/entities</code> 응답에 <code>qualifier</code> 추가, <b>정확일치 우선 정렬</b>.<br>
<b>§5 오류·한도</b> 문서화.<br><code>/v1/lookup/bulk</code> 에 <code>verified_only</code>·<code>status</code> 추가 — 단건과 계약이 같아졌습니다(권장 경로에 게이트가 없던 결함).<br><code>/v1/lookup/bulk</code> 의 <code>queries</code> 가 <b>객체</b>를 받습니다 — 이름마다 <code>type</code>·<code>context</code>. 문자열도 그대로 동작합니다.<br>조회 응답에 <code>status: ambiguous</code> 추가 — 동명이 둘 이상이면 고르지 않고 후보를 전부 돌려줍니다.<br><b>§8-0</b> 신설 — 기사 본문·제목을 보내지 않는 연동 순서.<br>
<code>include_absent</code>·<code>locales</code> 를 <code>/v1/lookup</code>·<code>/v1/lookup/bulk</code> 에 추가하고,
<code>/v1/prepare</code> 의 <code>missing</code> 에 <code>absent_locales</code> 를 붙였습니다 —
match 에만 있던 안내가 우리가 권하는 문에는 없었습니다.<br>
교정 자동반영이 <b>앵커 없는 대상에는 적용되지 않습니다</b> — 이름이 같다는 것은 같은 대상이라는
증거가 아닙니다(실측 사고 1건 회수).</td></tr>
</table>

<h2>10. 바뀐 것을 어떻게 아시나요 — 판본과 되물음</h2>

<div class="note warn"><b>★<code>out_of_scope</code> 를 캐시해 두셨다면 이 절을 꼭 읽어 주세요.</b>
문서는 그 상태를 "재조회 불필요"라고 적어 두었고, 그래서 많은 소비자가 캐시하고 다시 묻지 않습니다.
그런데 <b>우리 규칙이 바뀌면 그 판정은 만료됩니다.</b> 만료된 줄 모르면, 우리가 고친 것이
영영 닿지 않습니다 — 실제로 2026-09-15 범위 확대 때 그런 일이 있었습니다.</div>

<h3>① 모든 응답에 <code>X-KDB-Rules</code> 헤더가 옵니다</h3>
<pre>X-KDB-Rules: <!--RULES-VERSION--></pre>
<p>규칙이 바뀔 때만 바뀝니다. <b>마지막으로 보신 값과 다르면</b> 캐시해 두신 종결 답이 낡았다는 뜻입니다.
(<code>X-KDB-Version</code> 은 다른 값입니다 — 데이터가 바뀌면 바뀌므로 거의 매번 달라집니다.
규칙 변경 감지에는 <code>X-KDB-Rules</code> 를 쓰세요.)</p>

<h3>② <code>GET /v1/changelog</code> — 무엇이 바뀌었고 다시 물어야 하는지 (무인증)</h3>
<pre>{ <span class="k">"current"</span>: <span class="s">"<!--RULES-VERSION-->"</span>,
  <span class="k">"changes"</span>: [
    { <span class="k">"version"</span>:<span class="s">"<!--RULES-VERSION-->"</span>, <span class="k">"date"</span>:<span class="s">"2026-09-16"</span>,
      <span class="k">"summary"</span>:<span class="s">"..."</span>,
      <span class="k">"affects"</span>:[<span class="s">"out_of_scope"</span>,<span class="s">"review"</span>], <span class="k">"reask"</span>:true } ] }</pre>
<p><code>reask: true</code> 면 <code>affects</code> 에 있는 상태로 받아 두신 답을 버리고 다시 물어 주세요.</p>

<h3>③ <code>GET /v1/my/changes</code> — <b>당신이 물었던 것 중</b> 달라진 것</h3>
<p>어느 것을 다시 물어야 하는지 <b>우리가 계산해 드립니다.</b> 소비자 키로 부르시면
그 키로 물었던 낱말 중 답이 달라진 것만 옵니다. 하루 한 번이면 충분합니다.</p>
<pre>GET /v1/my/changes?since=2026-09-09T00:00:00Z

→ { <span class="k">"rules_version"</span>: <span class="s">"<!--RULES-VERSION-->"</span>,
    <span class="k">"next_since"</span>: <span class="s">"2026-09-16T11:00:00+09:00"</span>,
    <span class="k">"changes"</span>: [
      { <span class="k">"term"</span>:<span class="s">"이재명"</span>, <span class="k">"was"</span>:<span class="s">"out_of_scope"</span>, <span class="k">"now"</span>:<span class="s">"ready"</span>,
        <span class="k">"why"</span>:<span class="s">"now_served"</span>, <span class="k">"kid"</span>:<span class="s">"K0002690"</span>, <span class="k">"type"</span>:<span class="s">"person"</span> },
      { <span class="k">"term"</span>:<span class="s">"테일즈런너"</span>, <span class="k">"was"</span>:<span class="s">"out_of_scope"</span>, <span class="k">"now"</span>:<span class="s">"reask"</span>,
        <span class="k">"why"</span>:<span class="s">"rule_version_expired"</span> } ] }</pre>
<table>
<tr><th><code>now</code></th><th>하실 일</th></tr>
<tr><td class="ok">ready</td><td>바로 다시 조회하시면 값이 나옵니다</td></tr>
<tr><td>preparing</td><td>되살아나 준비 중입니다 — 잠시 후 다시</td></tr>
<tr><td>reask</td><td>그때의 판정이 만료됐습니다 — 다시 보내 주시면 지금 규칙으로 다시 판단합니다</td></tr>
</table>
<div class="note warn"><b>★이력은 <u>API 키 단위</u>입니다.</b> 키를 새로 발급받으시면
이 문이 돌려줄 이력도 그때부터 새로 시작됩니다 — 옛 키로 물으셨던 것은 따라오지 않습니다.
키를 바꾸실 계획이면 <b>바꾸기 전에</b> 한 번 부르셔서 미결을 받아 두시거나, 운영자에게 알려 주세요.</div>

<p class="sub"><code>since</code> 는 RFC3339, 생략하면 30일, 최대 90일. 한 번에 500건까지 오고
잘리면 <code>truncated: true</code> 가 붙습니다. 다음 호출에 <code>next_since</code> 를 넣으세요.
<b>달라진 것이 없으면 빈 배열</b>입니다 — 그게 "볼 것 없음"의 정직한 답입니다.</p>

<div class="note"><b>반대 방향은 이미 열려 있습니다.</b> 우리 값이 틀렸거나 근거가 있으시면
<code>POST /v1/corrections</code> 로 보내 주세요 — 30일 3,820건이 그렇게 들어왔고 2,759건은 자동 반영됐습니다.
<code>GET /v1/corrections/{id}</code> 로 처리 결과를 되물으실 수 있습니다.</div>

<p class="sub" style="margin-top:40px">문의: 운영자 발급 API 키 필요. 빈 결과는 에러가 아니라 빈 배열로 반환됩니다.
이 문서와 실제 동작이 다르면 그것은 우리 결함입니다 — 알려 주세요.</p>
</div></body></html>`
