# KDB handoff — 24차 (2026-06-29): 소스 파이프라인 확장 (iTunes+langlink-upgrade) + 지속 발굴 자동화

23차 [[KDB.handoff-20260629-2.md]] 이어짐. 오너 위임: "소스가 없다는 결론은 성급 — admin/settings 활용, 발굴하며 검증된 새 소스를 계속 파이프라인에 추가, 단계별로 결국 찾아낸다. 자리 비우니 전부 빌드·버그수정·검증·배포·continuous 자동화까지." 자율 완수 세션.

## §0 — 상태 체크
```sh
curl -s -o /dev/null -w 'api=%{http_code}\n' http://127.0.0.1:9100/v1/health   # 200
docker exec kdb-db psql -U kdb -d kdb -At -c "SELECT count(*) FROM (SELECT unnest(ARRAY[canonical_en_source,canonical_ja_source,canonical_vi_source,canonical_id_source,canonical_es_source,canonical_pt_br_source,canonical_zh_source,canonical_zh_hant_source]) s FROM kwave_entities WHERE status='active') t WHERE s='codex-fallback';"
```

## §1 — 결론 (TL;DR)
- **"소스 천장"은 틀린 결론이었음을 실증.** admin/settings 인벤토리에 미구축 권위소스 4개(iTunes·Wikidata-kana(P1814)·VIAF·Discogs)가 "계획"으로 박혀 있었고, 이건 "소스 없음"이 아니라 **파이프라인 미연동**. → 2개를 연동(iTunes, langlink-upgrade) + autopilot 지속가동.
- **4 병렬 발굴에이전트(haiku) 셀단위 실측**: iTunes song_album 62%(ja64·zh59) 충당가능 + **아티스트 앵커 100% 제공**(A3 "song_album 불가능" 반증) / zhwiki 35%(A2 "2%" 17.5배 오류) / ja 니체인물 0%(P1814·VIAF·jawiki 전부 실패 — 진짜 천장) / 스코프누수 ~7%.
- codex 8,038 → (이번 세션 추가 드레인 진행 중). 핵심은 숫자보다 **소스 파이프라인 영구 확장 + 지속 자동화**.

## §2 — 빌드·배포 완료 (HEAD 이후)
- **L1 langlink-upgrade** (`enrich/orchestrator.go::DrainLanglinkUpgrade` + CLI `langlink-upgrade [n]`): QID 사이트링크(zhwiki=zh_hant 주수율)로 codex 셀 교체. ★기존 enrich 는 langlink 를 applyEmptyOnly(빈칸전용)라 codex 못 건드림 = 파이프라인 갭 → 이 drain 이 #3 ko-label 매칭 게이트 위에서 codex 교체. **679 QID-codex 전수 처리 후 포화**(쿨다운 14d). 수율은 D2 낙관(35%)보다 낮음 — 모집단이 zh-codex 가 아니라 전체 QID-codex 라(대부분 codex 가 sitelink 없는 ja/vi/es locale).
- **L2 iTunes** (신규 `internal/kdb/itunes/` 클라이언트 + `itunes_drain.go::DrainITunesSongs` + CLI `itunes-songs [n]` + `SourceITunes` prio4): song_album ja/zh/zh_hant codex 를 국가스토어 제목으로 **confirm-only 승급**(값불변, source→itunes=검증등급) + **artistName 을 external_ref(itunes)로 저장 = song_album 의 결손이던 아티스트 앵커 확보**(향후 MusicBrainz/Discogs improve 기반). 영어/로마자 제목 곡이 주대상. 30d 쿨다운, 2.5s pacing. ★드레인 실행은 시간소요(2.5s×곡) — 이 문서 작성 시점 백그라운드 진행 중.
- **L3 스코프**: 인테이크 K-SCOPE 필터(prompts.go:154-165, 국적/K-연관+예외)와 cycle `stepScopeReview`(비-K 플래그, ★자동reject 안 함)가 **이미 구축·작동 확인**(D4 누수예시 디플로·탕웨이·앤해서웨이 등 5/5 [scope:review] 플래그됨, 총 229 플래그·2,328 [scope:ok]). **자동reject 안 함**(오너 부재+K-pop 외국적멤버 오거부 위험). 안전 최적화: langlink/itunes 드레인이 [scope:review] 엔티티 제외(무권위 lookup 낭비 방지).
- **★continuous 자동화** (`cmd/kdb/main.go::runAutonomousSourceExpand`, autopilot Hermes cycle 편입): 매 cycle romanize+reattribute·opencc·langlink-upgrade(30)·itunes(10) 소량 드레인. 쿨다운이 처리분 스킵→미처리만 점진(자기수렴). `KDB_SOURCE_EXPAND_ENABLED=0` 로 끌 수 있음(기본 ON). Hermes run row "SourceExpand"로 감독.
- api.go: 'itunes' 를 provenance external-db + verified set 에 추가(localeProvenanceLabel Go미러 + SQL localeProvenanceExpr/VerifiedExpr) → iTunes-confirmed 는 verified_only 통과.

## §3 — 정직한 잔존 (진짜 천장)
- **ja 니체 한국 인물 (person ja-codex 500, QID보유 228)**: D3 정밀재측(85 QID 실측, 재시도로 85/85 성공) — ja-codex 의 67%(1,720)는 이미 wikidata-label 보유(유명인), 남은 500이 갭. 그 500에 대해 **P1814(가나)·P1559·jawiki ≈0%**(기능복구 0/78), **VIAF IP차단(403)+도서관저자목록이라 트로트/코미디언/야구선수 부적합**. Wikidata 복구가능 ≈3/500(0.6%). mislink 8%(#3 ko-게이트 유효 재확인). → codex 가나표기가 사실상 유일/정답이나 검증불가, grounded enrich(gemma) 외 경로 없음. verified_only 게이팅으로 비노출.
- **★다음 소스 후보 — 실측으로 빌드 거부됨(헛수고 방지)**: ~~Wikidata-kana(P1814)~~ ≈0.6% 수율·~~VIAF~~ IP차단+부적합·~~Discogs~~ 연동했으나 confirm 0/200(라틴제목=iTunes 완전중복). **즉 ja/song 꼬리의 안전 권위레버는 전수소진**(iTunes+Discogs+MusicBrainz+Wikidata 3~4중 확인). 패턴(발견→실측검증→provider+drain+source_priority+autopilot, iTunes=템플릿)은 확립 — ★단 신규 소스는 반드시 실측 수율 먼저 확인 후 빌드(0이면 보고). 남은 꼬리는 gemma grounded enrich(상시 가동) + verified_only 게이팅으로 관리가 정답.

## §4 — 배포/커밋
- `docker restart kdb-app`로 재배포 완료(api/admin 200, 새 심볼 DrainLanglinkUpgrade·DrainITunesSongs·runAutonomousSourceExpand 확인). autopilot SourceExpand 다음 cycle부터 가동.
- 신규/수정 파일: `internal/kdb/itunes/itunes.go`(신규)·`internal/kdb/itunes_drain.go`(신규)·`enrich/orchestrator.go`·`source_priority.go`·`cmd/kdb/main.go`·`internal/kdbapi/api.go`·`internal/kdbadmin/sources.go`.
- 빌드: `docker exec -w /app kdb-app sh -c 'GOMODCACHE=/app/.gomodcache GOFLAGS=-mod=mod go build -o /tmp/kdb-app ./cmd/kdb'`. 재배포=`docker restart kdb-app`.

## §5 — 운영자 대기 작업
- **scope:review 229건 검토**(자동reject 보류 — K-pop 외국적멤버 오거부 위험, 오너 판단 필요). admin 대시보드 [11].
- iTunes improve(다른 번역제목)는 아티스트 앵커 확보분(external_ref itunes) 기반으로 향후 구현 가능(현재 confirm-only).

연관: [[KDB.handoff-20260629-2.md]] [[KDB.handoff-20260629.md]] [[reference-kdb-handoff]] [[feedback-honest-visibility]].
