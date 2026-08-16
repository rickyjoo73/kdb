package kdb

// mbgroup_anchor_drain.go — unverified **group** 에 MusicBrainz 아티스트 앵커를 붙인다.
// 키 없음(1 req/s). 승급도 로케일 채움도 하지 않는다 — ref 만 붙인다.
//
// ★왜 만들었나(2026-08-17). 겨냥 오류의 **네 번째** 판이다. `DrainMusicBrainzCandidates`
// 는 `status='candidate' AND entity_type='group'` 만 본다 — 즉 "후보를 active 로 올릴까"를
// 묻는 레인이라, **이미 active 인데 unverified 인 그룹 153건**은 한 번도 물어본 적이 없다.
// (같은 형태: 19번 Google News · 22번 iTunes · 23번 TMDb.)
//
// ★★국적 증명을 country 에서 **한글 별칭**으로 바꾼 것이 이 레인의 핵심이다(25건 실측).
//
//	종전 규칙(ko 검색 + country:KR)                     1건
//	country 조건 제거                                   9건 — 그중 8건이 오답
//	country=KR ∨ 한글 별칭 일치                          2건 — 오답 0
//
// country 를 그냥 빼면 `턴즈`→Turns(러시아 밴드), `AGD`→AGD(폴란드 그룹),
// `들고양이들`→Wildcats(old-time music), `케이보이즈`→K Boys(mixers) 가 들어온다.
// 반대로 country 만 믿으면 **MB 에 country 가 비어 있는 정답**을 놓친다 —
// `호라이즌`→HORI7ON 이 그랬다(한글 별칭 `호라이즌` 은 갖고 있다).
// **"한국 그룹인가"를 MB 의 국가 필드가 아니라 그 항목이 한국어 표기를 갖고 있는가로 묻는다.**
//
// ★`musicbrainz` 는 authoritativeIdentityProviders 다(source_policy.go). 앵커 하나로
// 곧바로 authoritative 이므로 확신이 없으면 기각한다 — 22번의 다섯 가드가 준 교훈이다.

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb/musicbrainz"
)

// mbGroupAnchorField — 원장 field. 성공·기각 모두 남긴다.
const mbGroupAnchorField = "mb-group-anchor"

// mbGroupMinRunes — 정규화 이름이 이보다 짧으면 아예 묻지 않는다.
//
// 1~2글자는 식별정보가 거의 없어 정확일치가 곧 우연이다(iTunes 레인의 `BE`→은혁 전례).
// 한글 별칭 증명이 있어도 짧은 이름은 우연히 겹칠 수 있어 애초에 물어보지 않는다.
const mbGroupMinRunes = 3

// MBGroupAnchorStats — 한 회차 판정 분포. 채택되지 않은 이유를 전부 센다
// (조용한 누락 금지 — "0건"이 무매칭 때문인지 가드 때문인지 구분되어야 한다).
type MBGroupAnchorStats struct {
	Checked    int // 조회한 엔티티
	Anchored   int // ref 를 붙인 엔티티
	TooShort   int // 이름이 짧아 묻지 않음
	NoMatch    int // 정확일치 결과 없음
	Ambiguous  int // 정확일치가 2건 이상 → 보류
	NoKorProof int // 정확·유일 일치했으나 한국 그룹 증명 실패 → 보류
	Failed     int // MB 호출 실패 — 마킹하지 않고 다음 회차
}

// DrainMBGroupAnchors — active·unverified group 의 MusicBrainz 앵커 사각지대를 닫는다.
// dry=true 면 검색·판정·로그만 하고 DB 쓰기가 전혀 없다(원장 마킹도 안 한다).
func DrainMBGroupAnchors(ctx context.Context, pool *pgxpool.Pool, cl *musicbrainz.Client, limit int, dry bool) MBGroupAnchorStats {
	var st MBGroupAnchorStats
	if pool == nil || cl == nil {
		return st
	}
	if limit <= 0 {
		limit = 20
	}
	// 대상: active·미잠금 group 중 등급이 unverified 이고 musicbrainz ref 가 없는 것.
	// ★승급 드레인(musicbrainz_drain.go:56)과 **겹치지 않는다** — 저쪽은 candidate 전용이다.
	rows, err := pool.Query(ctx, `
SELECT e.id::text, e.canonical_ko, COALESCE(e.canonical_en,'')
  FROM kwave_entities e
 WHERE e.status='active'
   AND e.operator_locked = false
   AND e.entity_type = 'group'
   AND e.verification_tier = 'unverified'
   AND COALESCE(e.canonical_ko,'') <> ''
   AND NOT EXISTS (SELECT 1 FROM kwave_entity_external_refs r
                    WHERE r.entity_id = e.id AND r.provider = 'musicbrainz')
   AND `+FillRetryPredicate("e", "'"+mbGroupAnchorField+"'")+`
 ORDER BY e.created_at ASC
 LIMIT $1`, limit)
	if err != nil {
		log.Printf("kdb.mb-group-anchor: select: %v", err)
		return st
	}
	type row struct{ id, ko, en string }
	var items []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.id, &r.ko, &r.en) == nil {
			items = append(items, r)
		}
	}
	rows.Close()

	for _, it := range items {
		if ctx.Err() != nil {
			break
		}
		// 질의어는 ko·en 둘 다 쓴다. 어느 쪽이 MB 의 표기인지 미리 알 수 없다 —
		// 실측에서 `JUSTB` 는 ko 로, `HORI7ON` 은 en 으로 걸렸다.
		terms := queryTerms(it.ko, it.en)
		if len(terms) == 0 {
			st.TooShort++
			if !dry {
				MarkFillAttempt(ctx, pool, it.id, mbGroupAnchorField, "name-too-short",
					fmt.Sprintf("정규화 이름이 %d자 미만이라 조회하지 않음", mbGroupMinRunes))
			}
			continue
		}
		st.Checked++

		matches := map[string]musicbrainz.Artist{}
		failed := false
		for _, term := range terms {
			artists, serr := cl.SearchGroupsAnywhere(ctx, term)
			if serr != nil {
				// ★전송 실패는 내용 판정이 아니다 — 마킹하지 않고 다음 회차로 넘긴다.
				failed = true
				log.Printf("kdb.mb-group-anchor: 조회 실패 %q — 마킹 없이 다음 회차 (%v)", term, serr)
				break
			}
			for _, a := range artists {
				if isMusicBrainzGroupMatch(term, a) {
					matches[a.ID] = a
				}
			}
		}
		if failed {
			st.Failed++
			continue
		}

		switch len(matches) {
		case 0:
			st.NoMatch++
			if !dry {
				MarkFillAttempt(ctx, pool, it.id, mbGroupAnchorField, "no-match",
					"MB 그룹 검색에 정규화 정확일치(score≥90) 없음")
			}
			continue
		case 1:
			// 계속
		default:
			// ★유일하지 않으면 고르지 않는다 — 동명 그룹에 최상위 등급을 추측으로 주지 않는다.
			st.Ambiguous++
			if !dry {
				MarkFillAttempt(ctx, pool, it.id, mbGroupAnchorField, "ambiguous",
					fmt.Sprintf("정확일치 MB 그룹이 %d건 — 동명 보류", len(matches)))
			}
			continue
		}

		var best musicbrainz.Artist
		for _, a := range matches {
			best = a
		}

		proof, perr := koreanGroupProof(ctx, cl, best, it.ko)
		if perr != nil {
			st.Failed++
			log.Printf("kdb.mb-group-anchor: 별칭 조회 실패 %q — 마킹 없이 다음 회차 (%v)", it.ko, perr)
			continue
		}
		if proof == "" {
			st.NoKorProof++
			log.Printf("kdb.mb-group-anchor%s: 한국 그룹 증명 실패 보류 ko=%q → mb:%s(%s, country=%q)",
				dryTag(dry), it.ko, best.Name, best.ID, best.Country)
			if !dry {
				MarkFillAttempt(ctx, pool, it.id, mbGroupAnchorField, "no-korean-proof",
					fmt.Sprintf("정확·유일 일치했으나 country≠KR 이고 한글 별칭도 없음: %s(%s)", best.Name, best.ID))
			}
			continue
		}

		if dry {
			st.Anchored++
			log.Printf("kdb.mb-group-anchor[dry]: 앵커후보 %s → %s (%s, 증명=%s)",
				it.ko, best.Name, musicbrainz.ArtistURL(best.ID), proof)
			continue
		}

		// 승급 드레인과 같은 confidence(0.75)·payload 모양 — 같은 가드를 통과한 값이다.
		tag, ierr := pool.Exec(ctx, `
INSERT INTO kwave_entity_external_refs
  (entity_id, provider, external_id, url, confidence, raw_payload, fetched_at)
VALUES ($1,'musicbrainz',$2,$3,0.75,$4,now())
ON CONFLICT DO NOTHING`, it.id, best.ID, musicbrainz.ArtistURL(best.ID),
			fmt.Sprintf(`{"resource_type":"artist","name":%q,"country":%q,"disambiguation":%q,"score":%d,"proof":%q}`,
				best.Name, best.Country, best.Disambiguation, best.Score, proof))
		if ierr != nil {
			// 쓰기 실패도 내용 판정이 아니다 — 마킹하지 않는다.
			st.Failed++
			log.Printf("kdb.mb-group-anchor: ref insert 실패 ko=%q: %v", it.ko, ierr)
			continue
		}
		if tag.RowsAffected() > 0 {
			st.Anchored++
			log.Printf("kdb.mb-group-anchor: 앵커 부착 %s → %s (%s, 증명=%s)",
				it.ko, best.Name, best.ID, proof)
		}
		MarkFillAttempt(ctx, pool, it.id, mbGroupAnchorField, "anchored",
			fmt.Sprintf("MB 정확·유일 일치 + %s → %s", proof, best.ID))
	}

	if st.Checked > 0 || st.TooShort > 0 {
		log.Printf("kdb.mb-group-anchor%s: checked=%d anchored=%d (무매칭=%d 동명=%d 증명실패=%d 짧음=%d 실패=%d)",
			dryTag(dry), st.Checked, st.Anchored, st.NoMatch, st.Ambiguous, st.NoKorProof, st.TooShort, st.Failed)
	}
	return st
}

// queryTerms — 검색어 목록(ko·en, 중복·짧은 것 제거). 비어 있으면 묻지 않는다.
//
// ★en 을 버리는 경우가 있다(2026-08-17 실측). **canonical_ko 에 한글이 없으면 그 ko 가
// 곧 정식 표기다.** 그런데 canonical_en 이 그와 다르면, 그 en 은 표기 변환이 아니라
// **다른 대상**일 수 있다 — 우리 `MIDZY`(ITZY 의 **팬덤명**)의 canonical_en 이 `Itzy` 였고,
// 그 en 으로 물어보니 MB 의 ITZY 가 정확·유일·country=KR 로 걸려 앵커가 붙었다.
// 팬덤은 아티스트가 아니다. 엔티티 노트에도 `disambiguator: distinct (팬덤명)` 이 있었다 —
// **레인은 자기가 받은 값만 믿었고, 그 값이 오염돼 있었다.**
//
// ko 에 한글이 있으면(`티에프앤`↔`TFN`, `호라이즌`↔`HORI7ON`) en 은 정상적인 로마자
// 표기이므로 그대로 쓴다. 이 규칙이 막는 건 **"라틴 ko 인데 en 이 딴 이름"** 하나다.
func queryTerms(ko, en string) []string {
	ko, en = strings.TrimSpace(ko), strings.TrimSpace(en)
	if en != "" && !hasHangulRunes(ko) && !musicbrainz.NameMatches(ko, en) {
		en = ""
	}
	var out []string
	for _, t := range []string{ko, en} {
		if t == "" || len([]rune(t)) < mbGroupMinRunes {
			continue
		}
		if !sameNameAsAny(out, t) {
			out = append(out, t)
		}
	}
	return out
}

// sameNameAsAny — 이미 물어보기로 한 이름과 **매처와 같은 규칙으로** 같은가.
// 여기서 EqualFold 를 쓰면 `Team H`/`team-h` 를 다른 질의로 보고 rate-limited 호출을
// 한 번 더 태운다 — 정작 매칭은 둘을 같다고 본다. 규칙을 두 벌 두지 않는다.
func sameNameAsAny(arr []string, s string) bool {
	for _, x := range arr {
		if musicbrainz.NameMatches(x, s) {
			return true
		}
	}
	return false
}

// koreanGroupProof — 이 MB 항목이 **한국 그룹임을 증명**하는 근거를 돌려준다(없으면 "").
//
// 두 가지만 인정한다:
//
//	country=KR                     MB 가 한국으로 명시한 경우
//	한글 별칭이 우리 ko 와 일치      country 가 비어 있어도 그 항목이 우리 이름으로 불린다
//
// ★두 번째가 왜 안전한가. 별칭 일치만으로는 부족하고 **그 별칭이 한글이어야** 한다.
// 우리 canonical_ko 가 라틴 문자면(`AGD` `Team H`) 같은 철자가 외국 그룹에도 흔해서
// 별칭 일치가 국적 증명이 못 된다 — 실측에서 폴란드 AGD·일본 Team H 가 그 형태였다.
// 그때는 country=KR 만 인정한다.
func koreanGroupProof(ctx context.Context, cl *musicbrainz.Client, a musicbrainz.Artist, ko string) (string, error) {
	if strings.EqualFold(strings.TrimSpace(a.Country), "KR") {
		return "country=KR", nil
	}
	if !hasHangulRunes(ko) {
		return "", nil // 한글이 아닌 이름은 별칭 증명을 쓸 수 없다 — country 만이 근거다.
	}
	aliases, err := cl.AliasNames(ctx, a.ID)
	if err != nil {
		return "", err
	}
	for _, al := range aliases {
		if hasHangulRunes(al) && musicbrainz.NameMatches(al, ko) {
			return "한글별칭=" + al, nil
		}
	}
	return "", nil
}

// hasHangulRunes — 완성형 한글 음절이 하나라도 있는가(kowiki 레인의 isHangul 재사용).
func hasHangulRunes(s string) bool {
	for _, r := range s {
		if isHangul(r) {
			return true
		}
	}
	return false
}
