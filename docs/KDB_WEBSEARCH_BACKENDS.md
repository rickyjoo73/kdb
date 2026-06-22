# KDB 웹검색 백엔드 — 실측·대안 (2026-06-22)

목적: 현지표기(local-usage) 발굴·classify 보조·site_search 가 쓰는 웹검색을 **KDB 서버에서
직접·on-demand·차단 없이** 수행. 기존엔 Google News RSS 의존 → KDB IP 에서 503 차단.

## 실측 (KDB 서버 IP 114.203.210.52, 컨테이너=호스트 동일 egress)

| 엔진 | HTTP | 결과 | 판정 |
|---|---|---|---|
| Google News RSS | **503** | — | ❌ 차단(기존 site_search·news_search 가 전부 이걸 씀 → 자체검색 죽어있었음) |
| DuckDuckGo lite | 200→**202** | 단일 200, ~5연속 후 202 지속 | ⚠️ DC IP 봇탐지·차단 지속 (fallback 한정) |
| **Bing** | **200** | b_algo 파싱 OK, ck/a URL 디코딩 | ✅ **주력** |
| Mojeek | 200 | clean, 독립 크롤러 | ✅ 후보(무료 API 有) |
| **Sogou(zh)** | 200 | result 60+ | ✅ 중국어 현지표기 최적 |
| Coccoc(vi) | 200 | 도달 | ◐ 베트남 현지(파서 필요) |
| Brave HTML | 200 | 됨 | ◐ API 권장 |
| Yandex | 302 | 리다이렉트 | ◐ consent 우회 필요 |
| Baidu / Yahoo!JP | 302 / 400 | — | ❌ DC IP 차단 / 파라미터 |

**결론: 단일 엔진·벌크는 막힌다.** provider chain + 전역 throttle + 차단 시 cooldown 으로
"필요할 때마다 1건씩" 하면 KDB 자체로 검색 가능(server22 불요). 단 스크래핑은 본질적으로
취약 → **정식 Search API(키)가 견고한 종착점.**

## 구현 (이번 세션, 라이브)
`internal/kdb/websearch` — `WebSearcher` provider chain.
- 전역 throttle(`KDB_WEBSEARCH_MIN_INTERVAL_MS`, 기본 2500ms): 모든 호출 직렬화 → 버스트 차단 방지.
- provider cooldown(`KDB_WEBSEARCH_COOLDOWN_MIN`, 기본 15m): 차단(비200) 감지 시 그 provider 스킵 → 체인 다음으로, 만료 시 자동 복귀.
- providers(`KDB_WEBSEARCH_PROVIDERS`, 기본 `bing,ddg`): Bing(b_algo + ck/a base64 URL 디코딩)·DDG lite.
- 배선: `news_search.SearchNewsContext`(classify 보조)·`site_search.searchDomain`(도메인 스코프 `site:` enqueue). 둘 다 Google News RSS → 이 체인.

## 권고 (단계)
1. **단기(완료)**: Bing 주력 + DDG fallback, throttle/cooldown. 기존 503 해결, server22 의존 완화.
2. **현지엔진 확장**: locale 별 provider 추가 — zh/zh_hant=**Sogou**(200·result多), vi=**Coccoc**. `KDB_WEBSEARCH_PROVIDERS=bing,sogou,ddg` 식 체인 + locale 분기. (현지표기 정확도↑.)
3. **장기(견고)**: 정식 **Search API 키** 발급 → 스크래핑 차단 영구 제거.
   - **Brave Search API**(무료 2k/월), **Bing Web Search API**(Azure), **Mojeek API**, Serper/SerpAPI(유료 Google).
   - Provider 1개 추가로 드롭인: `type braveProvider struct{key string}` + `Search()` 에서 `api.search.brave.com/res/v1/web/search` 호출. 체인 맨 앞.

## Provider 추가법
`websearch.go` 에 `xProvider` 타입 + `Name()`/`Search()` 구현, `Default()` switch 에 등록.
HTML 스크래퍼는 `fetch()`(UA·2MB cap·비200=error) + 정규식 파싱 재사용. API provider 는
key 를 env 에서 읽고 JSON 파싱(스크래핑 불요). cooldown/throttle 은 체인이 자동 적용.

## 안전/주의
- 스크래핑 = best-effort. 파서 깨지면 그 provider 만 빈 결과 → 체인이 다음으로.
- DC IP 는 어떤 엔진이든 대량 시 플래그됨. **on-demand·소량 원칙 필수**(벌크 금지).
- 현지표기 1차는 Wikidata/Wikipedia/TMDb/MusicBrainz(무료·무블록 API)가 이미 다수 커버 →
  웹검색은 그 나머지 long-tail 만 = 자연히 소량.
