package kdbadmin

import kdbroot "github.com/rickyjoo73/kdb/internal/kdb"

// sources.go — KDB 가 다국어 표기를 확보하는 모든 데이터 소스 인벤토리(관리자 settings 하단 표시).
// 오너 지시(2026-06-28): 우리가 확보할 수 있는 모든 관련 소스를 한곳에 가시화.
// source_priority.go 의 source enum + 확보 로드맵(docs/runbooks/KDB_ACQUIRE_ROADMAP_20260628.md) 동기.

// sourceInfo — 소스 한 행(표시용).
type sourceInfo struct {
	Name          string // 표시명
	Key           string // canonical_<loc>_source 값 / 식별자
	Pipeline      string
	Provides      string // 무엇을(어떤 타입·locale) 제공하나
	Tier          int    // source_priority(낮을수록 우선)
	Status        string // live | new | exp | plan
	Bulk          string // 벌크 안전성
	AutoPromote   bool
	ReviewOnly    bool
	DiscoveryOnly bool
	Note          string
}

type sourcePipelineInfo struct {
	Key        string
	Name       string
	Purpose    string
	Bottleneck string
	Sources    []sourceInfo
}

// sourceStatusBadge — Status → (라벨, tailwind class).
func sourceStatusBadge(st string) (string, string) {
	switch st {
	case "live":
		return "연동", "bg-emerald-100 text-emerald-800"
	case "new":
		return "신규", "bg-cyan-100 text-cyan-800"
	case "exp":
		return "실험", "bg-amber-100 text-amber-800"
	case "plan":
		return "계획", "bg-slate-100 text-slate-500"
	}
	return st, "bg-slate-100 text-slate-500"
}

// sourceInventory — 전체 데이터 소스 목록(우선순위 tier 순). 새 소스 추가 시 여기에 등록.
func sourceInventory() []sourceInfo {
	policies := kdbroot.SourcePolicies()
	rows := make([]sourceInfo, 0, len(policies))
	for _, p := range policies {
		rows = append(rows, sourceInfo{
			Name:          p.Name,
			Key:           p.Key,
			Pipeline:      p.Pipeline,
			Provides:      p.Provides,
			Tier:          p.Tier,
			Status:        p.Status,
			Bulk:          p.Access,
			AutoPromote:   p.AutoPromote,
			ReviewOnly:    p.ReviewOnly,
			DiscoveryOnly: p.DiscoveryOnly,
			Note:          p.Note,
		})
	}
	return rows
}

func sourcePipelines() []sourcePipelineInfo {
	rows := sourceInventory()
	byKey := map[string][]sourceInfo{}
	for _, r := range rows {
		byKey[r.Pipeline] = append(byKey[r.Pipeline], r)
	}
	defs := kdbroot.SourcePipelineDefinitions()
	out := make([]sourcePipelineInfo, 0, len(defs))
	for _, d := range defs {
		out = append(out, sourcePipelineInfo{
			Key:        d.Key,
			Name:       d.Name,
			Purpose:    d.Purpose,
			Bottleneck: d.Bottleneck,
			Sources:    byKey[d.Key],
		})
	}
	return out
}
