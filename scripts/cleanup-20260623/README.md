# QID 중복쌍 정리 (2026-06-23) — 15차 deferred 처리

동일 Wikidata QID 를 가진 active 엔티티 17쌍을 검증 후 정리.
**14차 교훈 준수**: 무검증 자동병합 금지 — 각 쌍을 Wikidata 레이블(`wbgetentities`)+
엔티티 상세(역할·대표작·소속·alias)로 검증해 **병합 vs 오염분할** 분류.

- `qid_dedup.sql` — 실행 스크립트(gitignore: *.sql). `kdb_merge_by_id(keeper,loser)` 함수 + 13 병합 + 4 분할.
- `snapshot_before.csv` — 적용 전 34행 스냅샷(revert 근거).

## 병합 13 (동일 엔티티·thin dup → keeper 로 통합, loser=rejected)
같은 한국어명·동일 type 의 명백 중복 + 그룹의 잘못된 type(song_album/event_tour) 재생성분.
keeper 는 권위(refs 다수·operator_locked·그룹 한국어명) 기준.

| QID | keeper | loser(rejected) |
|---|---|---|
| Q101161289 | 윈터 (refs9) | 윈터/WINTER |
| Q12601728 | 선우용여 (refs6) | 선우용여 |
| Q496439 | 한가인 | 한가인 |
| Q624351 | 황신혜 | 황신혜 |
| Q60550265 | 투모로우바이투게더 | 투모로우바이투게더 |
| Q624509 | KBS2 | KBS 2TV |
| Q653836 | 아리랑 | ARIRANG |
| Q109375061 | 아이브 (group, locked) | IVE |
| Q113189277 | 뉴진스 (group, locked) | New Jeans (song_album) |
| Q123865358 | 넥스지 (group) | NEXZ (event_tour) |
| Q13813305 | 베스티 (group) | Bestie (song_album) |
| Q486153 | 슈퍼노바 (group) | Supernova (song_album) |
| Q60528927 | 원어스 (group) | ONEUS (event_tour) |

## 오염분할 4 (서로 다른 인물이 나쁜 QID 공유 → 잘못된 쪽 wikidata ref DELETE, 병합 X)
Wikidata 가 확인해준 진짜 주인에게만 QID 유지. 반대쪽은 별개 active 인물로 보존.

| QID (Wikidata=진짜 주인) | keep | QID 제거(별개인물) |
|---|---|---|
| Q17198771 = Park Sung-hoon, 가수+피겨스케이터 = ENHYPEN 성훈 | 성훈/Sunghoon | 박성훈 (배우, Squid Game 2) |
| Q493329 = 10cm (musical duo) | 10CM | 권정열 (멤버 개인) |
| Q105717901 = Heeseung (singer) = ENHYPEN 이희승 | 희승 | 에반/Evan (별개 가수) |
| Q26710983 = Choi Yoo-jung (b.1999) = Weki Meki | 최유정 | 유정 = 남유정(브레이브걸스) + Choi 오염 alias 정리 |

## 검증
- 적용 후 동일 QID active 쌍 = **0**. 분할 4쌍 QID 가 진짜 주인에만 위치, 반대쪽 (none).
- 병합 패자 13 = 전부 rejected(notes `[kdb:qid-dedup]`). active 총수 4084→4071. API/admin 200.

## Revert
- 병합: `UPDATE kwave_entities SET status='active' WHERE id='<loser>'` (notes `[kdb:qid-dedup]` 검색). 단 media/refs 는 keeper 로 이동됨 — 완전복원은 snapshot 참조 수동.
- 분할: `snapshot_before.csv` 의 (entity_id, external_id) 로 wikidata ref 재INSERT.
