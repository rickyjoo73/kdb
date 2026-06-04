package main

// import-kenterhub — kenterhub.com /api/celebrities 덤프(JSON 배열)를 KDB
// candidate 로 등록하는 일회성 서브커맨드.
//
// 정공법: 직접 INSERT 하지 않고 kdb.CandidateStore.Observe 경로를 재사용한다.
// 따라서 gatekeeper.PreGate(자소 깨짐/문장 조각 거부)·동명이인 안전(같은
// canonical_ko 가 이미 2개 이상이면 needs_disambig 만 켜고 누적 보류)·active
// row 보호(locale fill 은 status='candidate' 에만) 가 그대로 적용된다.
//
// 매핑: nameKo→canonical_ko, nameEn→canonical_en(source rss-observation:
// kenterhub.com), nameJa→canonical_ja. source_domain="kenterhub.com".
// 단일 소스라 자동 promote(≥2 매체) 는 일어나지 않는다 — 기존 RSS 매체가 같은
// 이름을 발견하면 합의로 active 승격되고, 그 전엔 candidate 큐에 머문다.

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb"
)

type kenterCeleb struct {
	NameKo string `json:"nameKo"`
	NameEn string `json:"nameEn"`
	NameJa string `json:"nameJa"`
}

func runImportKenterhub(ctx context.Context, pool *pgxpool.Pool, path string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("import-kenterhub: read %s: %v", path, err)
	}
	var rows []kenterCeleb
	if err := json.Unmarshal(raw, &rows); err != nil {
		log.Fatalf("import-kenterhub: parse %s: %v", path, err)
	}

	cand := kdb.NewCandidateStore(pool)
	const src = "kenterhub.com"

	seen := make(map[string]bool, len(rows))
	var observed, dupSkip, emptySkip, fail int
	for i, r := range rows {
		ko := strings.TrimSpace(r.NameKo)
		if ko == "" {
			emptySkip++
			continue
		}
		if seen[ko] {
			dupSkip++
			continue
		}
		seen[ko] = true

		// nameEn 동반: candidate row 의 canonical_en 을 함께 채운다.
		if err := cand.Observe(ctx, ko, "en", strings.TrimSpace(r.NameEn), src); err != nil {
			log.Printf("import-kenterhub: [%d] %q en err=%v", i, ko, err)
			fail++
			continue
		}
		// nameJa 가 있으면 같은 candidate 에 ja 표기도 누적(데이터셋엔 극소수).
		if ja := strings.TrimSpace(r.NameJa); ja != "" {
			if err := cand.Observe(ctx, ko, "ja", ja, src); err != nil {
				log.Printf("import-kenterhub: [%d] %q ja err=%v", i, ko, err)
			}
		}
		observed++
		if (i+1)%100 == 0 {
			log.Printf("import-kenterhub: %d/%d ...", i+1, len(rows))
		}
	}
	log.Printf("import-kenterhub: done total=%d observed=%d dup-skip=%d empty-skip=%d fail=%d",
		len(rows), observed, dupSkip, emptySkip, fail)
}
