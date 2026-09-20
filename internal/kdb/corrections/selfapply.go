package corrections

// selfapply — **근거가 명확하면 우리가 확인하고 자체적으로 고친다.**
//
// ★오너 지시 (2026-09-20): "수정요청이 들어오면 근거가 명확하면 우리 에이전트가
// 확인하고 자체적 수정을 할 수 있도록 해줘."
//
// ★지금까지 막혀 있던 자리 (같은 날 실측). 판정기가 «클라 제안도 현재값도 아니고
// 제3의 값이 맞다»(verdict=other)고 답하면 그 값을 `proposed` 로 회신하고 **클라가
// 확인해 주기를 기다린다.** 그런데 소비자는 그 확인 API 를 부르지 않는다 —
// 접수 4,040건 중 클라가 확인한 것은 0건이고, 48시간 뒤 자동 적용된 것이 107건이다.
// 즉 실제 경로는 «이틀 기다렸다가 우리가 한다» 였다. 그 이틀 동안 소비자는 틀린
// 값을 받고, 같은 신고를 다시 보낸다(나 혼자 산다 ja 18회 · en 16회).
//
//	★근거가 이미 명확한 경우까지 기다릴 이유가 없다. 둘 중 하나면 즉시 반영한다:
//	  ① 우리 수정안이 **그 대상의 위키데이터 라벨/사이트링크와 일치**한다
//	     (이름 검색이 아니라 그 대상이 가진 QID 로만 본다 — 에반 사고 이후의 규칙)
//	  ② 빈칸이고 클라가 **신뢰 도메인 근거**를 함께 보냈다(빈칸>틀린값 위반 아님)
//	그 외에는 종전대로 proposed 로 두고 48시간을 기다린다. 근거 없는 즉시반영은
//	«단일 클라이언트 주장만으로 바꾸지 않는다»는 이 패키지의 신뢰 모델을 깬다.
//
// ★그리고 **우리 수정안이 우리 문자셋 규칙을 어기는 경우**를 조용히 넘기지 않는다.
//
//	실측 #3867: 「나 혼자 산다」 zh 에 판정기가 `我獨自生活`(번체)을 제안했다. 간체
//	칸이라 IsValidSpellingForLocale 가 거절하고, DrainProposed 는 `continue` 로
//	조용히 넘겼다 — 로그도 사유도 없이 3일 17시간. 7일째엔 "클라이언트 7일 미응답"
//	이라는 **사실과 다른 사유**로 강등될 예정이었다. 클라는 응답했고, 심지어 간체로
//	올바르게(我独自生活) 제안했다.
//
//	고치는 방법은 가드를 푸는 것이 아니라 **값을 제 자체로 되돌리는 것**이다.
//	ZhToSimplified/ZhToTraditional 은 «그 자체에서만 쓰는 글자»만 바꾸므로 과변환이
//	구조적으로 불가능하다(朴→樸 사고 이후 이 함수만 쓴다).

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/rickyjoo73/kdb/internal/kdb"
)

// repairForLocale — 값이 그 칸의 문자셋 규칙을 어기면 **되돌릴 수 있는 만큼** 되돌린다.
// 반환: (쓸 수 있는 값, 고쳤는지). 규칙을 통과하면 (원값, false).
// 되돌릴 수 없으면 (원값, false) — 호출부가 사유를 남긴다.
func repairForLocale(locale, val string) (string, bool) {
	val = strings.TrimSpace(val)
	if val == "" {
		return val, false
	}
	loc := normLocale(locale)
	if kdb.IsValidSpellingForLocale(loc, val) {
		return val, false
	}
	switch loc {
	case "zh":
		if out, ok := kdb.ZhToSimplified(val); ok {
			if out = strings.TrimSpace(out); out != "" && kdb.IsValidSpellingForLocale(loc, out) {
				return out, true
			}
		}
	case "zh_hant":
		if out, ok := kdb.ZhToTraditional(val); ok {
			if out = strings.TrimSpace(out); out != "" && kdb.IsValidSpellingForLocale(loc, out) {
				return out, true
			}
		}
	}
	return val, false
}

// charsetNote — 왜 못 썼는지 사람이 읽을 한 줄. 「보류」라고만 쓰지 않는다 —
// 다음에 읽는 사람이 원인을 다시 파야 한다.
func charsetNote(locale, val string) string {
	loc := normLocale(locale)
	switch {
	case loc == "zh" && kdb.ContainsTradOnly(val):
		return "간체(zh) 칸에 번체 전용 글자 — 자체 변환도 불가"
	case loc == "zh_hant" && kdb.ContainsHansOnly(val):
		return "번체(zh_hant) 칸에 간체 전용 글자 — 자체 변환도 불가"
	}
	return loc + " 문자셋 가드 미통과"
}

// selfApplyReason — 자체 반영해도 되는가. 되면 원장에 적을 사유를 함께 돌려준다.
//
// ★«명확한 근거» 를 두 가지로만 정의한다. 늘리려면 여기 한 자리를 고친다.
func (s *Service) selfApplyReason(ctx context.Context, eid uuid.UUID, ko, locale, value, evidenceURL, current string) (string, bool) {
	// ① 그 대상의 위키데이터가 같은 값을 말한다.
	if ok, ev := s.corroborate(ctx, eid, ko, normLocale(locale), value); ok {
		return "Wikidata 교차검증 일치(" + ev + ") — 자체 반영(클라 확인 대기 없음)", true
	}
	// ② 빈칸 + 신뢰 도메인 근거. 덮어쓰기 위험이 없다(빈칸만 채운다).
	if strings.TrimSpace(current) == "" {
		if dom, okDom := trustedSourceDomain(evidenceURL); okDom {
			return "빈칸 + 신뢰출처(" + dom + ") — 자체 반영(클라 확인 대기 없음)", true
		}
	}
	return "", false
}

// evidenceURLOf — 그 신고가 함께 보낸 근거 URL(자체 반영 판단 입력).
func (s *Service) evidenceURLOf(ctx context.Context, id int64) string {
	var u string
	_ = s.Pool.QueryRow(ctx,
		`SELECT COALESCE(evidence_url,'') FROM kwave_kdb_corrections WHERE id=$1`, id).Scan(&u)
	return u
}

// ─── 이미 답한 신고 ─────────────────────────────────────────────────────────

// priorRuling — 같은 (대상·로케일·제안) 에 대해 우리가 이미 내린 판정.
type priorRuling struct {
	status             string
	resolution         string
	decidedAt          time.Time
	times              int  // 이번 것을 포함한 신고 횟수
	hadTrustedEvidence bool // 그때 신뢰 도메인 근거가 함께 왔었나
}

// priorDecisionWindow — 이 기간 안의 판정만 재사용한다. 그보다 오래됐으면 세상이
// 바뀌었을 수 있다(공식 제목이 나중에 생기는 일이 실제로 있다).
const priorDecisionWindow = 30 * 24 * time.Hour

// priorDecision — 종결된 직전 판정을 찾는다. 열려 있는 건(pending/proposed/verifying)은
// 위쪽 중복 차단이 이미 처리하므로 여기서는 보지 않는다.
func (s *Service) priorDecision(ctx context.Context, eid uuid.UUID, locale, suggested string) (priorRuling, bool) {
	var p priorRuling
	var trusted []string
	err := s.Pool.QueryRow(ctx, `
SELECT status, COALESCE(resolution,''), COALESCE(resolved_at, created_at),
       count(*) OVER () + 1,
       array_remove(array_agg(COALESCE(evidence_url,'')) OVER (), '')
  FROM kwave_kdb_corrections
 WHERE entity_id=$1 AND locale=$2 AND suggested_value=$3
   AND status IN ('rejected','auto_applied','approved')
   AND COALESCE(resolved_at, created_at) > now() - $4::interval
 ORDER BY COALESCE(resolved_at, created_at) DESC
 LIMIT 1`, eid, locale, suggested, priorDecisionWindow.String()).
		Scan(&p.status, &p.resolution, &p.decidedAt, &p.times, &trusted)
	if err != nil {
		return priorRuling{}, false
	}
	// 판정이 «반영»이었다면 값은 이미 들어가 있다. 그래도 재사용해서 답한다 —
	// 같은 값을 또 쓰는 것보다 «이미 반영됐다»고 알려 주는 편이 정확하다.
	for _, u := range trusted {
		if _, ok := trustedSourceDomain(u); ok {
			p.hadTrustedEvidence = true
			break
		}
	}
	return p, true
}

// itoaSmall — 사유 문자열용 작은 수.
func itoaSmall(n int) string {
	if n <= 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
