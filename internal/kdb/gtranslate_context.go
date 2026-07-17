package kdb

// gtranslate_context.go — 채움(쓰기 폴백) 맥락 문장 생성기(오너 지시 2026-07-16):
// 고정 템플릿 대신, 우리가 이미 아는 엔티티 정보(유형·역할·소속·그룹·대표작·동명
// 구분)를 담은 문장을 gemma 가 만들어 구글 번역에 보낸다 — 동명이인·일반명사 오역을
// 문장 맥락으로 차단. gemma 미설정/실패/검증 탈락 시 고정 템플릿 폴백(기존 동작 보존).

import (
	"context"
	"encoding/json"
	"log"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rickyjoo73/kdb/internal/kdb/gemma"
)

// GTranslateEntityContext — 채움 대상 엔티티에 대해 이미 알고 있는 정보.
type GTranslateEntityContext struct {
	EntityID     string
	Role         string
	Agency       string
	Groups       []string
	NotableWorks []string
	Disambig     string
	CategoryHint string
}

// gtranslateTypeKo — 프롬프트에 넣을 유형 한글 라벨.
var gtranslateTypeKo = map[string]string{
	"song_album": "곡/앨범", "drama": "드라마", "movie": "영화", "show": "TV 프로그램",
	"event_tour": "공연/행사", "person": "인물", "group": "그룹", "character": "캐릭터",
	"agency": "소속사/기획사", "brand_place": "브랜드/장소", "channel_outlet": "채널/매체",
}

var gtranslateAnyQuoteRe = regexp.MustCompile(`['‘’"“”]`)

var gtranslateSentenceSchema = []byte(`{"type":"object","properties":{"sentence":{"type":"string"}},"required":["sentence"]}`)

// gtranslateSentenceValid — LLM 문장 검증(오염 차단): 따옴표가 정확히 한 쌍이고 그
// 사이가 원문 term 과 정확히 일치해야 한다. 추출 정규식(최외곽 greedy)이 다른 구간을
// 잡는 사고와, term 변형(LLM 이 이름을 고쳐 쓰는 것)을 원천 차단. 한 문장·길이 상한.
func gtranslateSentenceValid(s, term string) bool {
	if s == "" || strings.ContainsAny(s, "\n\r") || utf8.RuneCountInString(s) > 140 {
		return false
	}
	qs := gtranslateAnyQuoteRe.FindAllStringIndex(s, -1)
	if len(qs) != 2 {
		return false
	}
	return strings.TrimSpace(s[qs[0][1]:qs[1][0]]) == strings.TrimSpace(term)
}

// gtranslateContextInfo — 알려진 정보를 "키=값; ..." 로 직렬화(빈 값 생략).
func gtranslateContextInfo(entityType string, gctx *GTranslateEntityContext) string {
	var parts []string
	if t := gtranslateTypeKo[entityType]; t != "" {
		parts = append(parts, "유형="+t)
	}
	if gctx.Disambig != "" {
		parts = append(parts, "구분="+gctx.Disambig)
	}
	if gctx.Role != "" && gctx.Role != "other" {
		parts = append(parts, "역할="+gctx.Role)
	}
	if gctx.Agency != "" {
		parts = append(parts, "소속="+gctx.Agency)
	}
	if len(gctx.Groups) > 0 {
		parts = append(parts, "그룹="+strings.Join(gctx.Groups, ","))
	}
	if n := len(gctx.NotableWorks); n > 0 {
		if n > 3 {
			n = 3
		}
		parts = append(parts, "대표작="+strings.Join(gctx.NotableWorks[:n], ","))
	}
	if gctx.CategoryHint != "" {
		parts = append(parts, "카테고리="+gctx.CategoryHint)
	}
	return strings.Join(parts, "; ")
}

// gtranslateContextSentence — gemma 로 맥락 문장 생성. ok=false 면 호출측이 템플릿 폴백.
func gtranslateContextSentence(ctx context.Context, term, entityType string, gctx *GTranslateEntityContext) (string, bool) {
	if gctx == nil || !gemma.Configured() {
		return "", false
	}
	info := gtranslateContextInfo(entityType, gctx)
	if info == "" {
		return "", false // 맥락이 없으면 LLM 을 부를 이유가 없음 — 템플릿으로 충분
	}
	prompt := `당신은 번역 전처리기다. 아래 한국 엔터테인먼트 고유명사가 구글 번역기에서 공식 영문 표기로 인식되도록, 대상의 정체가 분명한 자연스러운 한국어 한 문장을 만들어라.

규칙:
1) 이름을 작은따옴표로 감싸 원문 그대로 정확히 1회 포함하라: '이름'
2) 따옴표는 그 한 쌍만 사용한다(다른 따옴표·인용 금지).
3) 아래 '알려진 정보'에 적힌 것만 사용하라. ★정보에 없는 사실(가수명·그룹명·소속·대표작·수식)을 절대 지어내지 마라 — 확실하지 않으면 유형(곡, 드라마, 인물 등)만 언급한 짧은 문장으로 써라. 틀린 사실은 오역을 만든다.
4) 한 문장, 100자 이내, 줄바꿈 금지. 이름 철자를 바꾸지 마라.
출력(JSON 한 줄만): {"sentence":"..."}

이름: ` + term + `
알려진 정보: ` + info
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	raw, err := gemma.Complete(cctx, prompt, gtranslateSentenceSchema)
	if err != nil {
		return "", false
	}
	var out struct {
		Sentence string `json:"sentence"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return "", false
	}
	s := strings.TrimSpace(out.Sentence)
	if !gtranslateSentenceValid(s, term) {
		log.Printf("kdb.gtranslate: 맥락문장 검증탈락 ko=%q s=%q — 템플릿 폴백", term, s)
		return "", false
	}
	log.Printf("kdb.gtranslate: 맥락문장 ko=%q → %q", term, s) // LLM 출력 모니터링
	return s, true
}
