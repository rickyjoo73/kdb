# KDB handoff — 2026-06-14 (13차, dataqa↔enrich 무한 핑퐁 근본수정)

이 세션 핵심: **동명이인 오염이 "아직도 안 풀리던" 진짜 원인 = dataqa(gpt-5.5)는 매번 정확히
오염을 잡아 비웠지만, enrich 가 같은 값을 다시 채워 dataqa 가 또 비우는 무한 핑퐁**이었다.
LLM 판단은 처음부터 옳았고, 문제는 **enrich↔dataqa 사이 공유 기억(수렴 가드) 부재**였다.
`internal/kdb/enrich/orchestrator.go` 에 **suppression 수렴 가드**를 넣어 끊었다.

## 0. 30초 점검
```sh
cd /data/home2/kdb.aiinplanet.com
docker ps --filter name=kdb-app --format '{{.Status}}'
# 핑퐁 멎었는지: 단일 entity 가 또 반복 클리어되는지(>=5/24h 면 잔존)
docker exec kdb-db psql -U kdb -d kdb -t -A -F'|' -c "select
 (select count(*) from kwave_entities where status='active') active,
 (select count(distinct entity_id) from kwave_kdb_dataqa_log where created_at>now()-interval '24 hours') osc_24h,
 (select count(*) from (select entity_id from kwave_kdb_dataqa_log where created_at>now()-interval '24 hours'
   group by entity_id having count(*)>=5) t) still_looping;"
# dataqa-tick 추이(이전엔 매 tick contaminated=4 고정 반복 → 점차 0 로 수렴해야 정상)
docker logs --since 3h kdb-app 2>&1 | grep dataqa-tick | tail -10
```
세션 시작 시점: active **3229** · dataqa_log **8878행** · 24h 오실레이션 **545 entity**(단일 최다
**1311회**, 나비 es='Ella Gross'). 수정 배포 후 `still_looping` 은 시간이 지나며 0 으로 수렴해야 한다.

## 1. ★운영 핵심 (11~12차 계승, 변경 없음)
- **빌드 없는 배포**: 코드수정 → `docker restart kdb-app`(~12s, 소스 `.:/app` bind-mount, compose
  command 가 `go build→exec`). `docker compose build` 금지. env 변경만 `up -d kdb-app`.
- **LLM 하이브리드**: codex=고난도(dataqa·FillLocale·Disambig·corrections), gemma=대량(extract 등).
- **push**: `git -c credential.helper='!gh auth git-credential' push https://github.com/rickyjoo73/kdb.git HEAD:main`
- **auto-mode 차단 주의**: ① 프로덕션 DB **쓰기**(raw UPDATE)는 광범위하면 차단 — 구체 id/조건으로
  좁히거나 시스템 감사커맨드(`dataqa --apply`) 사용. ② DSN/비번을 **명령줄에 리터럴로 넣으면
  차단**(secret echo) — `docker exec kdb-db psql -U kdb -d kdb …`(소켓 trust)로 읽을 것.

## 2. 이번 세션 처리 완료
- **수렴 가드(핵심)** `internal/kdb/enrich/orchestrator.go`:
  - `snapshot.Suppressed map[locale]map[normVal]bool` 추가. `loadSnapshot` 이
    `kwave_kdb_dataqa_log`(verdict='contaminated' AND reverted_at IS NULL)에서 **이 entity 에서
    dataqa 가 비운 (locale,값)** 을 로드.
  - `normForSuppress` = dataqa `normExpr`(SQL) 및 wikidata/musicbrainz `normalizeName` 과 **동일
    문자셋 제거**(공백/중점/하이픈/마침표/언더스코어/따옴표/쉼표) → 표기차 흡수.
  - 3개 자동 쓰기 경로(`applyFromMap`=wikidata-label/musicbrainz/tmdb/kofic, `applyEmptyOnly`=
    langlink, `runCodexFallback`=codex)가 쓰기 직전 `snap.isSuppressed(loc,val)` 확인 → 일치하면
    skip(재주입 거부). operator 가 `dataqa.Revert` 하면 reverted_at 채워져 로드에서 빠지므로
    suppression 자동 해제(LLM 오판 안전판 유지).
  - 단위 테스트 `internal/kdb/enrich/suppress_test.go`(정규화·nil-safe).
- **검증(read-only, end-to-end)**: `dataqa --apply` 로 비운 직후 `enrich-test` 재실행 →
  | entity | 비웠던 오염값 | enrich 재실행 결과 |
  |---|---|---|
  | 연준(TXT) | es=`EVAN` | es=**`Yeonjun`**(langlink) — EVAN 차단 |
  | 채수빈 | es=`El amor de mi vidaa` | es=**`Chae Soo-bin`** — 문구 차단 |
  | 황희 | id=`Hwang Hee (politisi)` | id=**`Hwang Hee`**, zh=`黄熙` — 정치인 차단 |
  오염값만 정확히 막고 **올바른 langlink 값으로 수렴**(빈칸 아님). DB 최종 상태 확인 완료.

## 3. 근본 원인 (재발 방지용 기록)
무한 핑퐁의 두 축:
1. **공유 기억 부재** — dataqa 가 비워도 enrich 가 같은 Wikidata 항목에서 같은 값을 재fetch.
   나비("Navi", 한국 가수)를 Wikidata 검색 → 동명의 **Ella Gross**(한국계 미국 모델) 반환 →
   es/id/pt_br 오염 → dataqa clear → enrich refill → **433회**.
2. **자기강화 트랩** — 잘못된 QID 가 `kwave_entity_external_refs` 에 기록되자
   `orchestrator.qidConfirmed`→true 가 되어 `runWikidata` 의 동명이인 가드(homonym.Conflict)가
   **영구 스킵**. 수렴 가드는 가드 스킵 여부와 무관히 값 단계에서 막으므로 이 트랩도 무력화.

## 4. ★다음 세션 작업 후보
- **(옵션) 545 가속 정리**: 수정 후 정상 워커 사이클에서 1회씩 클리어되면 자동 수렴(재오염 불가).
  급하면 `drain-enrich` 로 백로그를 돌려 pending 화 → `dataqa --apply` 반복(codex 토큰 소모).
- **(옵션) 근본2 추가수정**: 자동 기록된 wikidata external_ref(confidence 0.85)를 `qidConfirmed`
  의 "확정"으로 보지 않게(operator/검증분만 확정) — 단 R3 과잉차단 회귀 주의. 현재는 수렴 가드로
  무해화돼 우선순위 낮음.
- 12차 잔여: song_album 검증율 14%(`drain-enrich`→🟢), API match/lookup `min_trust` 노출.
- 핸드오프가 SSOT — memory 에 진행상태 적지 말 것.

## 5. 운영 명령
- 배포: 코드 → `docker restart kdb-app`. env → `docker compose -f docker-compose.kdb.yml up -d kdb-app`.
- 검증툴: `docker exec -w /app kdb-app /tmp/kdb-app enrich-test <ko>`(단건 enrich 실측, 채운 값/source 출력),
  `... dataqa --apply`(감사 경로로 오염 정리, pending 소진까지 루프), `... dataqa`(dry-run).
- 빌드/테스트: `docker exec -w /app kdb-app sh -c 'go build ./... && go test ./internal/kdb/enrich/ ./internal/kdb/dataqa/'`.

## 6. 미해결/관망
- 545 오실레이션 잔여분 자동 수렴 중 — 30초 점검의 `still_looping` 으로 추적(0 수렴 확인).
- dataqa_log 8878행은 감사 이력으로 보존(되돌리기 근거). 신규 행은 수렴 후 급감해야 정상.
- 자율운영은 컨테이너 독립 가동(세션과 무관).

HEAD = `78770fa`. 연관: [[project-kdb-cutover]], [[reference-kdb-handoff]], [[reference-kdb-dataqa]].
