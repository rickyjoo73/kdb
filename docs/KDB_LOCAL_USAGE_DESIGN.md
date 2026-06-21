# KDB 다국어 현지표기 보강 — 재설계 (2026-06-21)

상태: **설계 (구현 전, 오너 검토용)**. 이전 검색-우선 설계를 폐기하고, 검증된 사실로 재작성.

## 목적
각 엔티티의 9개 locale(en/ja/vi/id/es/pt_br/zh/zh_hant) **현지 통용 표기**를 제공.
번역·음역 "지어내기" 금지 — 권위 소스에서 가져오거나, 현지에서 실제 쓰인 것을 검색으로 확인.

## 핵심 원칙 (이번 세션 검증)
1. **권위 구조화 DB가 1순위** — TMDb/Wikidata/MusicBrainz가 현지표기를 전문적으로 유지한다.
   실측: TMDb `/translations`에 오징어게임 pt_br=**Round 6**, es=El juego del calamar, vi=Trò chơi con mực 이미 존재.
2. **Gemma 검색은 그 DB들이 못 채운 빈 locale만 보강** (오너 지시: "업데이트 안된 것을 Gemma가 찾아 채우는 형식").
3. **무명 국내전용 꼬리는 어디에도 없음 → 빈칸 유지** (검색·DB 모두 0. 지어내면 오염).

## 계층 (위 = 권위, 빈칸 채움 + 하위 잘못값 교체)
```
1. 현지사용/운영자 잠금 (수동 확정)
2. 매체합의 (현지매체 2곳+)
3. TMDb        — 작품(drama/movie/show): translations + alternative_titles  ★현 미흡
4. Wikidata    — 전체: labels→canonical, aliases→aliases, sitelink→제목
   MusicBrainz — 인물/그룹: locale alias(primary→canonical, 그외→aliases)
5. Gemma 검색 보강 — 위가 못 채운 빈 locale만 (서버22·DDG·튜닝)  ★신규(codex-fallback 대체)
```

## A. TMDb 정상화 (작품 — 최대 효과, 즉시)
- **문제(진단됨)**: ① cascade가 "빈 locale 있을 때만" 작동 → 9칸이 (틀린 wikidata값으로라도) 차면 TMDb 안 돎(오징어게임 pt_br=Squid Game 미교정). ② `pickMatch` 한국어 제목 정확일치만 → 매칭 누락(작품 807개 중 240개만 TMDb).
- **재설계**:
  - 작품 전수 재적용 패스(`kdb-app tmdb-refresh [n]`): 빈칸뿐 아니라 **wikidata/codex가 채운 값도 TMDb 우선순위(4<5)로 교체**.
  - `/translations` + **`/alternative_titles`** 둘 다 (alt에 TW=魷魚遊戲 등). en-copy 가드 유지(영어복사는 빈칸).
  - 매칭 완화: 정확일치 우선 + (보조) original_title/연도 매칭. 오매칭 가드는 유지.
- 파일: `internal/kdb/tmdb/tmdb.go`, `internal/kdb/enrich/orchestrator.go(runVideoAPIs)`, `cmd/kdb`.

## B. Wikidata / MusicBrainz (전체 + 인물·그룹)
- Wikidata client는 이미 labels+aliases+sitelinks fetch(`wikidata/client.go:116`). **aliases가 `aliases_<locale>`에 영속화되는지 점검 후 보강.**
- MusicBrainz: locale alias의 **primary 플래그**를 canonical로, 나머지를 aliases로 (NewJeans=ニュージーンズ 류).

## C. Gemma 검색 보강 (빈 locale만 — 마지막 계층)
- **위치 = 서버22**: 22 IP는 Google에 덜 막히고(단 대량이면 막힘→DDG 사용), **Gemma 로컬:8080(키 불필요)**. 검색+추출 자체완결, 결과를 KDB로.
- **검색 = DuckDuckGo lite** (Google News는 대량 시 어느 IP든 503 차단 — 폐기). **소량·throttle 필수**.
- **검색어 = 이름 + 영문 + 역할 + 대표작/소속(KDB person_details 메타)** → 동음이의 차단(유정→油井 방지, 박성훈 운동선수 혼동 방지).
- **추출 프롬프트(5라운드 튜닝 완료)**:
  - 안티-동음이의(about_entity): 검색결과가 진짜 이 한국 {type/대표작}에 관한 것인지 먼저 판단, 무관/동음이의면 found=false.
  - native_form/latin_form 분리, **가장 자주 등장**한 것, 결과에 **글자그대로 있는 것만(grounding)**, native는 현지문자만(한글 금지).
- **영속화**: native→`canonical_<loc>`, latin(≠native)→`aliases_<loc>`. char-set 가드·dataqa억제·빈칸우선. source=`local-usage`(또는 검색보강 전용 라벨).

## 우선순위/가드 (기존 재사용)
- `internal/kdb/source_priority.go` Priority/ShouldReplace, `IsValidSpellingForLocale`, en-copy 가드, `isSuppressed`. 신규 source 추가 시 Go Priority ↔ SQL `kdb_source_priority` 동기화.

## 정직한 커버리지
- 유명/중간급(클라 요청 대상): TMDb/Wikidata/MusicBrainz로 대부분 채움. 검색은 그 위 보강.
- 무명 국내전용 꼬리(니치 예능·단명 인물): 어떤 소스에도 없음 → **빈칸 유지**.

## 실행 순서 (각 단계 소량검증 → 전수, 커밋·배포 전 diff 리뷰)
1. **TMDb 정상화** (작품 대량 교정 — 오징어게임 Round 6 즉시). 가장 큰 효과.
2. **Wikidata aliases 영속화 점검·보강**.
3. **Gemma 검색 보강** (서버22·DDG·빈칸만) — 1·2 후 남은 gap.
4. MusicBrainz locale-alias 보강.
