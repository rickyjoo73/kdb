// Package aijudge — LLM 판단 (분류 / 다국어 합성) wrapper.
// gpt-5.5 가 분류 / 다국어 합성 둘 다 처리. extractor.go 와 같은 패턴.
//
// 이전엔 codex-bridge HTTP endpoint 를 호출했으나, 이제 codex CLI 를 직접
// exec (internal/kdb/codexcli). public 시그니처 + 4 struct 는 그대로.
// codex 실패 시 fallback 동작 (Classify→unknown, FillLocale→error 비치명)
// 도 이전 bridge 동작과 동일하게 유지.
package aijudge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/rickyjoo73/kdb/internal/kdb/codexcli"
)

// Client — codex CLI 호출자.
type Client struct {
	Runner *codexcli.Runner
}

// New — codexcli.NewRunner 기반 client.
func New() *Client {
	return &Client{Runner: codexcli.NewRunner()}
}

// --- /kdb_classify --------------------------------------------------------

// ClassifyInput — entity 분류 요청.
type ClassifyInput struct {
	Ko            string            `json:"ko"`
	Spellings     map[string]string `json:"spellings,omitempty"`
	SourceDomains []string          `json:"source_domains,omitempty"`
	Notes         string            `json:"notes,omitempty"`
	Wikidata      *ClassifyWikidata `json:"wikidata,omitempty"`
	SearchHits    []string          `json:"search_hits,omitempty"`
}

type ClassifyWikidata struct {
	QID         string `json:"qid"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
}

// ClassifyResult — 응답.
//
// 동명이인 구분용 보조 필드(Agency/NotableWorks/BirthYear)는 person 일 때
// 확신하는 경우에만 채워진다. 비어있으면 미상으로 취급(persist 시 무시).
//
// PR3 (design §B.1): Gender/Groups/SecondaryRoles 추가 — classify 시점에
// 포착한 인물 사실을 버리지 않고 kwave_entity_person_details 에 기록.
// 이 세 컬럼은 지금까지 writer 가 없어 groups 가 68% 비어 있었음 (design A.2).
// 모두 person + 확신 시에만 채워지며, 비면 persist 가 기존 값을 보존.
type ClassifyResult struct {
	EntityType     string   `json:"entity_type"`
	Confidence     float64  `json:"confidence"`
	Reason         string   `json:"reason"`
	PrimaryRole    *string  `json:"primary_role"`
	NeedsSearch    bool     `json:"needs_search"`
	Agency         string   `json:"agency,omitempty"`
	NotableWorks   []string `json:"notable_works,omitempty"`
	BirthYear      int      `json:"birth_year,omitempty"`
	Gender         string   `json:"gender,omitempty"`          // "M" | "F" | "" (미상)
	Groups         []string `json:"groups,omitempty"`          // 소속/활동 그룹명
	SecondaryRoles []string `json:"secondary_roles,omitempty"` // 보조 역할 (person_role enum)
}

// Classify — codex CLI 직접 호출 (구 /kdb_classify).
//
// PRESERVE: codex 실패 시 {entity_type:"unknown", confidence:0, reason:err}
// 를 반환 (이전 bridge 의 200 fallback 과 동일, 비치명).
func (c *Client) Classify(ctx context.Context, in *ClassifyInput) (*ClassifyResult, error) {
	if c == nil || c.Runner == nil {
		return nil, errors.New("classify: nil client")
	}
	var wd *codexcli.Wikidata
	if in.Wikidata != nil {
		wd = &codexcli.Wikidata{
			QID:         in.Wikidata.QID,
			Label:       in.Wikidata.Label,
			Description: in.Wikidata.Description,
		}
	}
	prompt := codexcli.BuildClassifyPrompt(in.Ko, in.Spellings, in.SourceDomains, in.Notes, wd, in.SearchHits)

	// 분류는 구조화 판정이라 gemma 로 충분(속도 우선). 고난도만 codex.
	// KDB_LLM_CLASSIFY=codex 로 개별 재정의 가능.
	raw, err := c.Runner.
		WithProvider(codexcli.RoleProvider("CLASSIFY", "gemma")).
		WithEffort(codexcli.RoleEffort("CLASSIFY", "medium")).
		Run(ctx, prompt, codexcli.ClassifySchema)
	if err != nil {
		// 이전 bridge fallback: {entity_type:"unknown", confidence:0, reason:err}.
		// 가시화(2026-06-20): 합성 unknown 으로 삼키되, LLM(Gemma/Codex) transport 실패는
		// 로그로 남긴다 — 안 그러면 게이트웨이 장애가 조용히 분류품질을 떨어뜨린다.
		// 모니터: grep 'kdb.classify: LLM transport 실패' 빈도로 게이트웨이 health 추적.
		log.Printf("kdb.classify: LLM transport 실패 ko=%q → 합성 unknown(분류 보류): %v", in.Ko, err)
		return &ClassifyResult{EntityType: "unknown", Confidence: 0, Reason: err.Error()}, nil
	}

	var r ClassifyResult
	if err := json.Unmarshal(raw, &r); err != nil {
		log.Printf("kdb.classify: 응답 디코드 실패 ko=%q → 합성 unknown: %v", in.Ko, err)
		return &ClassifyResult{EntityType: "unknown", Confidence: 0, Reason: fmt.Sprintf("classify decode: %v", err)}, nil
	}
	if r.EntityType == "" {
		return nil, errors.New("classify: empty entity_type")
	}
	return &r, nil
}

// --- /kdb_fill_locale -----------------------------------------------------

// FillInput — 빈 locale 합성 요청.
type FillInput struct {
	Ko          string            `json:"ko"`
	EntityType  string            `json:"entity_type,omitempty"`
	PrimaryRole string            `json:"primary_role,omitempty"`
	AliasesKo   []string          `json:"aliases_ko,omitempty"`
	Known       map[string]string `json:"known,omitempty"`
	Missing     []string          `json:"missing"`
	Wikidata    *ClassifyWikidata `json:"wikidata,omitempty"`
	Sitelinks   map[string]string `json:"sitelinks,omitempty"`
}

// FillResult — 합성된 spelling 들 + skipped locale.
type FillResult struct {
	Spellings []FilledSpelling `json:"spellings"`
	Skipped   []string         `json:"skipped"`
	Error     string           `json:"error,omitempty"`
}

type FilledSpelling struct {
	Locale     string  `json:"locale"`
	Value      string  `json:"value"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}

// FillLocale — codex CLI 직접 호출 (구 /kdb_fill_locale).
//
// PRESERVE: codex 실패 시 {spellings:nil, skipped:nil, error:err} 를 반환하고
// error 를 함께 반환 (비치명 — 이전 bridge 의 200 fallback + decode 후 Error!=""
// 처리와 동일 동작).
func (c *Client) FillLocale(ctx context.Context, in *FillInput) (*FillResult, error) {
	if c == nil || c.Runner == nil {
		return nil, errors.New("fill_locale: nil client")
	}
	var wd *codexcli.Wikidata
	if in.Wikidata != nil {
		wd = &codexcli.Wikidata{
			QID:         in.Wikidata.QID,
			Label:       in.Wikidata.Label,
			Description: in.Wikidata.Description,
		}
	}
	prompt := codexcli.BuildFillLocalePrompt(in.Ko, in.EntityType, in.PrimaryRole, in.AliasesKo, in.Known, in.Missing, wd, in.Sitelinks)

	raw, err := c.Runner.Run(ctx, prompt, codexcli.FillLocaleSchema)
	if err != nil {
		// 이전 bridge fallback: {spellings:[], skipped:[], error:err} → caller 가
		// Error!="" 로 비치명 처리.
		r := &FillResult{Spellings: nil, Skipped: nil, Error: err.Error()}
		return r, fmt.Errorf("bridge: %s", r.Error)
	}

	var r FillResult
	if err := json.Unmarshal(raw, &r); err != nil {
		r := &FillResult{Spellings: nil, Skipped: nil, Error: fmt.Sprintf("fill_locale decode: %v", err)}
		return r, fmt.Errorf("bridge: %s", r.Error)
	}
	if r.Error != "" {
		return &r, fmt.Errorf("bridge: %s", r.Error)
	}
	return &r, nil
}
