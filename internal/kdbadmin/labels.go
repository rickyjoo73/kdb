package kdbadmin

import "strings"

func commonKo(code string) string {
	switch code {
	case "review":
		return "검토 필요"
	case "confirmed":
		return "연결 검수됨"
	case "conflict":
		return "변경·충돌 재검토"
	case "binding_staged":
		return "원본 ID 관측"
	case "binding_changed":
		return "원본 정보 변경"
	case "candidate_registered":
		return "미검증 후보 등록"
	case "mapping_decision":
		return "연결 검수·철회"
	case "mapping_invalidated":
		return "원본 변경으로 연결 무효화"
	case "source_recheck_requested":
		return "독립 원천 재확인 요청"
	case "source_unavailable":
		return "원본 사용 불가 관측"
	case "source_refresh_scheduled":
		return "정기 독립 원천 갱신"
	case "source_policy_blocked":
		return "원본 출처 정책 보류"
	case "place_inactive":
		return "원본 삭제·비활성·병합"
	case "link_missing":
		return "기존 외부 ID 연결 소실"
	case "record_deleted":
		return "원출처 레코드 소실"
	case "type_unsupported":
		return "신규 원본 유형 검토 필요"
	case "source_category_conflict":
		return "원본 업종과 독립 출처 대상 유형 충돌"
	}
	if code == "policy:common-anchored-fill-v1" {
		return "검수된 정체성 기반 자동 확인"
	}
	if code == "operator_review" {
		return "운영자 검수"
	}
	if code == "unreviewed" {
		return "검수 전"
	}
	if code == "legacy-scope-review" {
		return "기존 범위 재검토"
	}
	labels := map[string]string{"": "미지정", "organization": "기관·조직", "company": "기업", "location": "장소", "work": "작품", "team": "팀", "league": "리그", "event": "대회·행사", "product": "상품", "concept": "개념", "politics": "정치", "government": "행정", "economy": "경제", "society": "사회", "entertainment": "연예", "sports": "스포츠", "travel": "여행", "culture": "문화", "active": "사용 중", "candidate": "미검증 후보", "rejected": "보류·기각", "retired": "사용 종료", "kdb": "기존 KDB", "tdb": "관광 TDB", "native": "공통 Entity", "canonical": "대표 표기", "alias": "별칭", "transliteration": "음역", "recorded": "원천 기록", "generated": "자동 생성", "translated": "번역 생성", "unknown": "미확인", "unverified": "미검증", "verified": "검증 기록 있음", "withdrawn": "철회", "blocked": "사용 차단", "operator-candidate": "운영자 후보 등록", "wikidata-label": "Wikidata 기록", "actor": "배우", "singer": "가수", "other": "기타"}
	if label, ok := labels[code]; ok {
		return label
	}
	return typeKo(code)
}
func commonDomains(codes []string) string {
	labels := make([]string, 0, len(codes))
	for _, code := range codes {
		labels = append(labels, commonKo(code))
	}
	if len(labels) == 0 {
		return "미지정"
	}
	return strings.Join(labels, ", ")
}

// labels.go — admin 화면의 원시 영문 코드(entity_type·origin·큐상태·게이트사유)를
// 운영자용 한글 라벨로 변환한다(오너 지시 2026-07-16: "영어 문구 모두 제거").
// 미등록 코드는 원문 그대로 반환 — 새 코드가 생겨도 화면이 깨지지 않는다.

// typeKo — entity_type 한글 라벨.
func typeKo(t string) string {
	switch t {
	case "person":
		return "인물"
	case "group":
		return "그룹"
	case "drama":
		return "드라마"
	case "movie":
		return "영화"
	case "show":
		return "예능·방송"
	case "song_album":
		return "곡·앨범"
	case "agency":
		return "소속사"
	case "channel_outlet":
		return "채널·매체"
	case "event_tour":
		return "공연·행사"
	case "brand_place":
		return "브랜드·장소"
	case "character":
		return "배역"
	case "term":
		return "일반어"
	case "unknown", "":
		return "미분류"
	}
	return t
}

// originKo — intake_origin 한글 라벨.
func originKo(o string) string {
	switch o {
	case "prepare":
		return "사전준비"
	case "lookup-miss":
		return "번역miss"
	case "correction-miss":
		return "교정파생"
	case "direct-api":
		return "직접호출"
	case "internal":
		return "내부"
	case "defense-test":
		return "테스트"
	case "unknown", "":
		return "미표기"
	}
	return o
}

// qStatusKo — research_queue status 한글 라벨.
func qStatusKo(s string) string {
	switch s {
	case "pending":
		return "대기"
	case "in_progress":
		return "발굴중"
	case "done":
		return "완료"
	case "failed":
		return "실패"
	}
	return s
}

// precheckKo — precheck_status(게이트 판정) 한글 라벨.
func precheckKo(p string) string {
	switch p {
	case "pass":
		return "통과"
	case "review":
		return "보류"
	case "reject":
		return "기각"
	case "approved":
		return "자동승인"
	case "legacy":
		return "구버전(미심사)"
	}
	return p
}

// precheckReasonKo — 게이트 판정 사유 코드 한글 라벨.
func precheckReasonKo(code string) string {
	switch code {
	case "duplicate_live_request":
		return "중복 요청 (이미 처리 중)"
	case "missing_or_unsupported_type":
		return "유형 불명·미지원"
	case "missing_exact_context":
		return "기사 맥락 부족"
	case "missing_source_evidence":
		return "출처 근거 없음"
	case "missing_type_context_cue":
		return "유형 단서 부족"
	case "conflicting_entity_types":
		return "유형 충돌"
	case "normalized_identity_conflict":
		return "동명 충돌 — 정체 확인 필요"
	case "type_shape_conflict":
		return "유형·형태 불일치"
	case "ambiguous_common_for_type":
		return "일반명사 가능성"
	case "commodity_term":
		return "광고·상거래어 기각"
	case "latin_passthrough":
		return "로마자 제목 — 번역 불필요(원문 사용)"
	case "triage_garbage":
		return "오염 판별 기각 (문장형·조합어)"
	case "existing_rejected_entity":
		return "기각 확정 (이미 기각된 키워드)"
	case "no_evidence_expired":
		return "근거 없음 21일 만료 — 자동 기각"
	case "single_char_needs_operator":
		return "한 글자 — 운영자 검토"
	case "category_not_entity":
		return "카테고리어 기각"
	case "term_not_proper_noun":
		return "일반어(고유명사 아님)"
	case "existing_entity":
		return "기존 엔티티"
	case "operator_approved":
		return "운영자 승인"
	case "typed_context_evidence":
		return "맥락 근거 통과"
	case "auto_evidence_encyc":
		return "자동승인 (백과 근거)"
	case "auto_evidence_news":
		return "자동승인 (뉴스 근거)"
	case "auto_evidence_news_translated":
		return "자동승인 (원제 재검색)"
	case "auto_evidence_web":
		return "자동승인 (웹검색·화이트리스트 매체)"
	case "empty":
		return "빈 키워드"
	case "unsafe_unicode":
		return "비정상 문자"
	case "no_letter":
		return "문자 없음"
	}
	return code
}
