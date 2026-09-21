package kdb

// locale_source_default — 새 개체는 로케일 출처를 **비워서** 태어난다.
//
// ★왜 (2026-09-21 45회차). 로케일 출처 컬럼 8개가 전부 `DEFAULT 'wikidata-label'` 이었다
//
//	(0049_kdb_provenance.sql). 그 마이그레이션은 컬럼을 **추가하면서 기존 행을 채우려고**
//	기본값을 줬는데, 기본값이 그대로 남아 이후의 모든 INSERT 에 붙었다. 개체를 만드는
//	세 경로(on-demand 발굴 · RSS 후보 · 수요 근거) 중 **어느 것도 출처를 넣지 않으므로**
//	새 개체는 값이 하나도 없는데 8칸이 전부 「위키데이터에서 왔다」고 주장했다.
//
//	실측: 9,527행 · 약 5만 4천 칸. zh_hant 만 봐도 active 2,217칸 중 1,336칸은 09-20
//	스냅샷에도 있었고, 876칸은 그 뒤 새로 생긴 개체였다 — **값을 지우는 곳은 없었다.**
//	오늘 만든 on-demand 발굴 250건이 전부 이 모양으로 태어났다(분당서울대병원 ·
//	카카오벤처스 · I'm Your Girl … 값 0칸 · 출처 wikidata-label 6칸).
//
//	기능은 틀어지지 않았다 — 채움 가드들은 「값이 빈칸이면 덮는다」를 먼저 본다. 틀어진
//	것은 **기록**이다. 빈 칸이 권위 출처를 주장하면 출처로 판단하는 모든 감사가 속는다.
//	43회차에 내가 이것 때문에 「누가 값을 지운다」고 오독할 뻔했다.
//
// ★기본값 자체도 0154 에서 '' 로 바꾸지만, 그 DDL 은 바쁜 테이블에서 잠금을 못 얻으면
//
//	건너뛰게 짜 두었다(배포 경로를 막지 않으려고). 그래서 **코드 쪽에서도 명시한다** —
//	기본값이 무엇이든 새 개체의 출처는 빈칸이다.

// NoLocaleSourceCols · NoLocaleSourceVals — INSERT 에 그대로 이어 붙이는 열·값 목록.
const (
	NoLocaleSourceCols = `canonical_en_source, canonical_ja_source, canonical_vi_source, canonical_zh_source, canonical_zh_hant_source, canonical_es_source, canonical_id_source, canonical_pt_br_source`
	NoLocaleSourceVals = `'', '', '', '', '', '', '', ''`
)
