# KDB 엔티티 품질 파이프라인 — 통합 마스터 설계 (2026-06-21)

상태: **설계 (구현 직전, 오너 확정 대기)**. 하위: KDB_LOCAL_USAGE_DESIGN(보강), KDB_CONTAM_AUDIT_DESIGN(정화).

## 목적 (한 줄)
각 엔티티의 ① **canonical_ko 가 정상**(오염 아님)이고 ② **9개 locale 이 정확한 현지 통용표기로 채워짐**.
번역·지어내기 금지 — 권위 DB에서 가져오고, 없으면 검색으로 현지 실제표기 확인.

## 통합 QA 흐름 (엔티티 1건)
```
[선정] ★전수 처리(모든 active). 우선순위 큐: 소비자요청(research_queue) > 빈칸/미답변 > 의심(오염) > 나머지 — 결국 전 엔티티 처리
   │
[S0 SQL 스크리닝] 한글누출·한국어복사·일반어형 등 → 명백건 빠른 분기
   │
[S1 한국어 정화]  Gemma 다회투표(temp0.8·N5, 서버22) + 검색veto(K범위)
   ├ 오염확정      → reject(+다국어 무효) + 감사로그
   ├ 범위밖확정    → 운영자 플래그
   ├ 경계(표갈림)  → gpt-5.5(Codex) 에스컬 → reject/플래그/keep
   └ real_k        → ↓
[S2 보강(빈 locale)]  TMDb(translations+alt) > Wikidata(label+alias+sitelink) > MusicBrainz > Gemma검색(서버22)
   │
[S3 기존값 검증]  Gemma 다회투표(영어복사·동음이의·스크립트오염) + 검색veto
   ├ 오염값        → 비우고 S2 재보강, 못고치면 플래그
   └ ok            → 완료
   │
[감사로그 + 운영자 검토큐]
```

## 정화/보강 공통 LLM 티어 (비용·정확도 균형)
1. **구조화 DB**(LLM 0) — TMDb/Wikidata/MusicBrainz. 가능한 건 여기서. 권위·견고.
2. **Gemma 다회투표**(서버22, 싸고 빠름) — temp0.8·N5. 만장일치=확정, 표갈림=경계.
3. **gpt-5.5(Codex)** — 경계 건만(비용). 최종 판정.
4. **운영자** — gpt5.5도 불확실하거나 자동삭제 위험한 건.

## 아키텍처 (검색은 반드시 서버22에서)
- **KDB Go(kdb-app)**: 선정·S0 SQL·S2 구조화DB·persistence·gpt5.5 에스컬(기존 aijudge codex)·감사로그·운영자 검토 UI.
- **서버22 워커**(신규): Gemma 로컬:8080 다회투표 + DDG 검색(22 IP·소량·throttle). S1 정화판정·S3 값검증·S2의 Gemma검색 담당.
- **통신**: 22 워커 ↔ KDB 공개API(kdb.aiinplanet.com). KDB가 작업큐(GET) 제공 + 결과 ingest(POST, X-KDB-Key). 22는 KDB 내부DB 직접 접근 안 함.

## 안전
- 경계는 **절대 자동삭제 X** (다회투표+gpt5.5+운영자 3중).
- 모든 자동 reject/clear = 감사로그(`kwave_kdb_dataqa_log` 류) → 되돌리기.
- 검색 veto = 신인(CORTIS) 오삭제 방지.
- 율제한: 서버22 DDG 소량·throttle·일일한도. gpt5.5는 경계만(비용캡).

## 검증 완료 (2026-06-21)
- TMDb 재적용: 오징어게임 pt_br=Round 6 등, 작품 241→337(`tmdb-refresh`, commit e459099).
- 검색veto: 코르티스/수목금/유어즈/모둡 등 신인·무명 정확 구제. 박인비(골프)/키린지(일본밴드) 범위밖.
- 다회투표 temp0.8: 경계(박인비·Lights·세계일주) 표갈림→에스컬, 명백(코르티스) 만장일치.

## 구현 순서 (각 단계 소량검증→전수, 커밋·배포 전 diff)
1. **S2 보강 완성** — TMDb 전수(완료분 확장) + Wikidata **aliases 영속화**(현재 버려짐) + MusicBrainz locale-alias.
2. **서버22 워커 + KDB ingest 엔드포인트** — 통신 골격.
3. **S1 정화**(다회투표+검색veto+gpt5.5 에스컬) — 오염 reject.
4. **S3 값검증** — 다국어 오염 정리.
5. 운영자 검토 UI + 감사로그.

## 확정 필요 (오너)
- 통신: 22워커+KDB ingest API (정석) vs 내가 SSH+psql 배치로 우선 결과부터.
- 자동화: 명백건 자동(reject/fill)+경계만 운영자 vs 전건 운영자 승인.
- 우선순위: research_queue 먼저 vs 저신뢰/의심 먼저 vs 전수.
