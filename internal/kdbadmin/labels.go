package kdbadmin

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
