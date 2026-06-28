# KDB handoff — 22차 (2026-06-29): 병목 진단 + 안전 권위레버 소진확인 + langlinks 안전성 실증

새 세션 첫 명령 = §0(21차 [[KDB.handoff-20260628.md]] 이어짐). 이 세션은 오너 위임("병목을 모두 해결, LLM 자가생성 절대금지, 공신력 사이트만") 자율작업.

## §0 — 상태 체크 (21차와 동일)
```sh
curl -s -o /dev/null -w 'api=%{http_code}\n' http://127.0.0.1:9100/v1/health
curl -s -o /dev/null -w 'admin=%{http_code}\n' http://127.0.0.1:9101/healthz
docker exec kdb-db psql -U kdb -d kdb -At -c "SELECT s,count(*) FROM (SELECT unnest(ARRAY[canonical_en_source,canonical_ja_source,canonical_vi_source,canonical_id_source,canonical_es_source,canonical_pt_br_source,canonical_zh_source,canonical_zh_hant_source]) s FROM kwave_entities WHERE status='active') t WHERE s IN ('codex-fallback','wikidata-label','tmdb','romanization','opencc','wikipedia-langlinks') GROUP BY s ORDER BY count(*) DESC;"
```

## §1 — 결론 요약 (TL;DR)
- **서버/워커 literal 병목 없음.** api/admin 200, kdb-app CPU~0(idle지 정체 아님), RSS 56피드 0에러, grounded enrich 실시간 작동(예: 록시 하트 zh 3/3 grounded), cycle 5~10분. **로그는 UTC(호스트 KST와 9h차) — "멈춘 듯" 착시 주의.**
- **진짜 병목 = codex-fallback(LLM 합성) 꼬리 11,2xx셀.** 오너 지시=권위소스로 교체, 자가생성 금지.
- **안전 권위레버 전수 소진 확인**: Wikidata QID-refill(`refill-anchored` 2/200)·romanize(+29)·opencc(+8)·TMDb(`tmdb-refresh` 0/80) 전부 거의 무수율. codex 11.2k 중 **wikidata QID 보유는 20개뿐** — 꼬리는 **무QID niche**라 안전 앵커 자체가 없음. ⇒ 21차 §6 "정직한 제약"을 정량 재확증.
- **codex 순감 11,261→11,209(-52)**: romanize/opencc/refill 결정적 드레인 + autopilot. 전부 100% 권위/결정적.

## §2 — ★Wikipedia langlinks(source_urls 기반) = 안전하지 않음 (실증·폐기)
무QID 꼬리(source_urls에 wikipedia.org URL 보유 1,412엔티티)를 MediaWiki langlink로 직접 채우는 도구를 구축·실행했으나 **오염 발생 → 전량 revert → 코드 제거**.
- **근본 결함**: source_urls의 wikipedia URL이 **엔티티와 불일치**(과거 research/enrich가 잘못 첨부) + **동명이인**. 실측 오염: 김윤지→정소민(庭沼珉)·최준희→최진실(Choi Jin-sil)·DK→Dplus Kia·민지→페기구·채연→정채연·프롬트웬티→래환·아현→BABYMONSTER(그룹)·채서안→변서윤. 동명이인 위험: 가비(영화)·이승윤(양궁선수) 등 이름은 같으나 다른 개체.
- **교훈**: langlink은 반드시 **QID 앵커**(ko-wiki→wikibase_item→QID ko-label==canonical_ko 검증) 위에서만 안전. source_urls/이름 검색 직접 신뢰 금지(넷플릭스 `resolveOTTID` ko-앵커 교훈과 동일 계열). 기존 enrich의 langlink는 이미 QID-sitelinks 경로라 안전 — **무QID 꼬리는 안전한 langlink 경로가 존재하지 않음**(QID가 없어서). ⇒ 무QID 꼬리 langlink은 닫힌 길.
- **처리**: `field='langlinks'` 쿨다운 529행은 내 도구 전용 식별자였고 **전량 삭제**. 오염 셀(50엔티티)은 enumerated-ID 스코프로 blank. wikipedia-langlinks 총계 235(baseline)→214(set-S에 걸쳤던 baseline 21셀도 같이 blank, QID-enrich로 자가복구됨). HEAD/코드 변경 없음(전부 revert, **HEAD=33b3f07, tree clean**).

## §3 — song_album 꼬리(316 ja/zh) = 권위소스 평가
- iTunes JP는 권위 제목 보유(불타오르네→"Burning Up (FIRE)", Seven→"Seven"). **단 song_album 엔티티에 아티스트 필드 부재** → 제목만 검색 시 동명곡/커버/리믹스 오매칭 위험. MusicBrainz는 원제(한국어)만 반환 → 현지제목 부적합.
- ⇒ 아티스트 앵커 없는 곡매칭은 "빈칸>틀린값" 위반 위험. **자동 구축 보류**(21차 §7 [계획] 유지가 정답). 향후 구축 시 필수=아티스트 MBID/그룹 앵커.

## §4 — 다음 세션 권장 (안전 범위)
- **현 상태가 안전 최대**(21차 §6 재확인). 꼬리는 grounded enrich(gemma+SearXNG 다회투표, 이미 가동)로 점진 + verified_only 게이팅(provenance=llm-only로 codex 소비자 미노출)으로 관리. 가속=검색 throughput↑(IP밴, 오너금지) or 직번역(오너금지) 외 없음.
- 손대지 말 것: source_urls/이름 검색 직접 채움(=오염). **무조건 ID/QID 앵커 + ko-제목 검증 + charset 가드(IsValidSpellingForLocale) 3중**일 때만.
- 잔존 선제거 권장(별건, dataqa 담당): 일부 pre-existing wikidata mislink 표면화(예: 프롬트웬티 es=Rae Hwan = 잘못된 QID). 이번 세션 범위 외 — homonym 가드/dataqa가 처리.
- 결정적 도구(romanize/opencc)는 autopilot 미편입(수동 CLI). 신규 검증 en/zh 유입분 자동 전파 원하면 autopilot step 추가 고려(소수 trickle, 우선순위 낮음).

## §5 — 빌드/운영 (21차 §0.5 동일)
GOMODCACHE=/app/.gomodcache 우회 필수. 수동 CLI=`docker exec kdb-app /tmp/kdb-app <cmd>`. 서비스 재시작 안 함(이 세션 코드변경 0). .env 런타임값=21차 §4.

연관: [[KDB.handoff-20260628.md]] [[reference-kdb-handoff]] [[reference-kdb-gemma-discovery]].
