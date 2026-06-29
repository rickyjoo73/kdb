# KDB handoff — 23차 (2026-06-29): codex 병목 멀티에이전트 해결(#1~6 로드맵 실행)

새 세션 첫 명령 = §0(22차 [[KDB.handoff-20260629.md]] 이어짐). 이 세션은 오너 위임("codex-fallback LLM 병목을 모든 에이전트로 대안 찾아 해결, 전체 로드맵 #1~6") 자율작업. ★코드변경 다수 — **미커밋 상태**(러닝 서비스는 재시작 시 /app 소스에서 재빌드되므로 이미 변경분 가동 중, 그러나 git 미반영 = 취약).

## §0 — 상태 체크
```sh
curl -s -o /dev/null -w 'api=%{http_code}\n' http://127.0.0.1:9100/v1/health   # 200
curl -s -o /dev/null -w 'admin=%{http_code}\n' http://127.0.0.1:9101/healthz   # 200
docker exec kdb-db psql -U kdb -d kdb -At -c "SELECT count(*) FROM (SELECT unnest(ARRAY[canonical_en_source,canonical_ja_source,canonical_vi_source,canonical_id_source,canonical_es_source,canonical_pt_br_source,canonical_zh_source,canonical_zh_hant_source]) s FROM kwave_entities WHERE status='active') t WHERE s='codex-fallback';"   # 8038
```

## §1 — 결론 요약 (TL;DR)
- **codex 11,188 → 8,038 (−3,150, −28.2%)**. 서비스 정상(api/admin 200, autopilot 재개).
- **22차 인계문서의 두 전제가 틀렸음을 실증 정정**: ① "QID 20개뿐" → 실제 codex 2,694엔티티 중 **1,425개 QID 보유**(refill이 0인 건 고갈이 아니라 14일 쿨다운+라벨부재). ② "더 할 게 없음" → **즉시 안전처리 3,145셀이 통째로 누락돼 있었음**.
- **병목 = 단일문제 아님, 3가지가 한 숫자에 뭉쳐있었음**: ①값정답·라벨오류(~3,145, 즉시정정) ②진짜 소스천장(~7,950, 채울 수 없는 게 정상) ③소비자노출(게이팅으로 해소).

## §2 — 멀티에이전트 진단 (4 병렬 general-purpose)
A1 라틴전파 / A2 QID라벨 / A3 대안소스 / A4 스루풋·게이팅. 각자 코드+DB 실측. 핵심:
- **A1/A4 공통**: person/group 라틴(es/id/pt_br/vi) codex 셀의 3,240/3,268이 권위 en과 **바이트동일** — codex가 이미 올바른 로마자 생성, 소스라벨만 오류. 기존 `DrainRomanizePersons` no-churn 게이트가 영구 스킵.
- **A2**: ja 라벨 usable 0/40·zh ~2%·zh_hant ~5%(Wikidata 40개씩 실측) — niche 꼬리는 권위 라벨이 세상에 없음. refill 14일 쿨다운 포화(2,603건).
- **A3**: KMDb/IMDb/AsianWiki/차트/방송사/Namuwiki 전부 부적합. TMDb 제목번역 이미 천장. song_album=관계테이블 0행이라 앵커불가.
- **A4**: 스루풋 병목 아님(Gemma 24×2·batch60 이미 상향, 적격풀 99.3% 쿨다운). codex 소비자 기본노출(pt_br 50%·vi 44%…), prepare/lookup 게이팅 전무.

## §3 — 실행 결과 (#1~6)
- **#1 로마자 재라벨** ✅ `romanize.go::DrainReattributeRomanization` 신규. person/group 라틴 codex 셀 중 값==canonical_en 인 것을 source=romanization 으로 재라벨(★값변경 0). **−3,145** (vi 865·id 711·pt_br 1065·es 504, dataqa 49 제외). +신규 fill 136(빈칸). `romanize-persons` CLI 에 wiring. 되돌리기 스냅샷: scratchpad relabel_revert CSV(3145행). ⚠️[[honest-visibility]]: 지표조작 아님 — 값이 권위 en의 결정적 로마자와 증명가능 동일, reattributeTMDb(6/24 승인) 선례.
- **#2 opencc + zh QID** ✅ `opencc-convert` 재실행 +8(−4 codex). refill-anchored 쿨다운 포화 확인(processed=0). 잔여 ~5 zh 업그레이드는 쿨다운 만료시 자동.
- **#3 ko-label 게이트** ✅ `orchestrator.go::runWikidata` 에 QID ko-label != canonical_ko 시 라벨임포트 차단(qidConfirmed/ko라벨부재는 면제). `wikidata.NormalizeName` export. 향후 enrich 방어심화(具俊曄 vs 具俊瞱·李成延 차단).
- **#4 게이팅 전환** ✅ `api.go`: prepare/lookup 에 verified_only + per-locale provenance 추가(기본 false=기존동작 보존). Entity 에 per-locale source 내부필드(json:"-") + LocaleProvenance. match 의 localeProvenanceExpr/localeVerifiedExpr 를 Go 미러(`localeProvenanceLabel`+`verifiedProvenances`, 단일진실원천). **실측 검증 통과**(prepare verified_only→codex ja missing, lookup verified_only→canonical_ja blank+locale_provenance). 테스트 추가(prepare_test.go).
- **#5 SearXNG 엔진** ✅(보수적) `websearch.go::searxngEngines`: 실측상 wikipedia=한국어쿼리 0반환·mojeek=CJK 0. **sogou(zh-native, 실측10)를 zh/zh_hant 추가**, bing 주력 유지. ★ddg/brave/google 은 working 이나 **오너 IP밴 금지로 미사용**(주 recall 레버이나 미적용 — 오너 명시승인 필요).
- **#6 song_album 관계** ✅(실측→빌드 NO-GO) 316건 중 QID 19(6%)·MB 0·관계 0행. 297건 무앵커=이름매칭 금지경로. 19건은 기존 refill 경로가 처리. 대형시스템을 6%커버·불확실수율로 짓는 건 효율/honest-visibility 위반 → **빌드 안 함이 정답**, 297건은 #4 게이팅으로 verified 소비자에게서 가려짐.

## §4 — 배포 상태 + 다음 세션 필수
- **러닝 서비스 = 변경분 가동 중**: kdb-app CMD 가 `/app` 소스에서 `go build` 후 exec → `docker restart kdb-app`로 재배포 완료(preflight plain build 통과). 새 심볼 확인(DrainReattributeRomanization·applyLocaleVerifiedGate).
- ★**코드 미커밋**(8파일: cmd/kdb/main.go·romanize.go·orchestrator.go·wikidata/client.go·websearch.go·kdbapi/api.go+prepare_test.go·go.mod[opencc indirect→direct 자동tidy, 무해]). **다음 세션 첫 작업 = 브랜치 생성 후 커밋**(main 직접 금지). 미커밋이면 git clean/checkout 시 유실 위험.
- 빌드: `docker exec -w /app kdb-app sh -c 'GOMODCACHE=/app/.gomodcache GOFLAGS=-mod=mod go build -o /tmp/kdb-app ./cmd/kdb'`. 서비스 재배포=`docker restart kdb-app`.

## §5 — 남은 ~8,000 codex의 정직한 처리
- **채울 수 없는 게 정상**(권위 데이터 부재, 오너 "빈칸>틀린값"). 드레인 백로그 아님 = 구조적 바닥.
- 관리: #4 게이팅으로 verified 소비자에게서 가림 + grounded enrich(쿨다운 만료분 점진). 추가 수율 레버는 ddg/brave/google(IP밴 트레이드오프, 오너승인 필요) 또는 직번역(금지) 외 없음.
- 소비자키별 verified_only 기본값(operator 정책): #4로 capability 는 갖춤, 키별 default 적용은 운영자 결정사항(미적용).

연관: [[KDB.handoff-20260629.md]] [[reference-kdb-handoff]] [[reference-kdb-gemma-discovery]] [[feedback-honest-visibility]].
