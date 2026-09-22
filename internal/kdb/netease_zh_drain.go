package kdb

// netease_zh_drain — 곡의 **중국어 제목을 중화권 음원 카탈로그에서 가져온다.**
//
// ★왜 (2026-09-23). 요청된 active 개체 중 zh 빈칸이 1,029건이고 곡이 157건으로 둘째였다.
//   위키데이터·TMDb 는 롱테일 K-곡을 모른다. 출처 정책표에는 「중화권 음원 카탈로그
//   (QQ/NetEase/Tencent) — 4등급, 자동 승급, 직역이 아니라 **플랫폼에 등록된 제목만**」이
//   계획으로 올라 있었는데 구현이 없었다.
//
// ★실측(2026-09-23, 표적 154곡):
//
//	한국어 제목 정확일치 107곡 → 기사 가수와 일치 40곡 → **중국어 번역제목 보유 11곡**
//	(+ 표기 변형까지 세면 15곡). 영문 번역제목만 있는 것 23곡은 zh 로 쓰지 않는다.
//
//   작아 보이지만 이 값들은 «플랫폼에 등록된 제목»이라 잠정값(llm-provisional 9등급)과
//   기계번역(8등급)을 자동으로 밀어낸다 — 채우는 레인이 아니라 **고치는 레인**이다.
//
// ★관문 셋. 하나라도 빠지면 동명곡을 가져온다(실측: 「안녕」·「동행」·「제일 잘 나가」는
//   제목만으로는 다른 가수의 곡이 먼저 나온다).
//
//	① 제목 정확일치(괄호 주석 제거 후)
//	② 가수 일치 — 기사에 함께 나온 active 인물·그룹, 또는 우리가 이미 가진 음원 앵커의 아티스트
//	③ 값이 한자여야 한다 — 플랫폼 번역제목에는 영문도 섞여 있다(Ghosting · Nice Day)

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// neteaseEndpoint — 웹 검색 API. 공개·무키. 예의상 호출 간격을 둔다.
const neteaseEndpoint = "https://music.163.com/api/cloudsearch/pc"

// neteaseUA — 이 엔드포인트는 브라우저 UA·Referer 가 없으면 암호화 응답을 준다.
const neteaseUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124 Safari/537.36"

type neteaseArtistRow struct {
	Name  string   `json:"name"`
	TNS   []string `json:"tns"`
	Alias []string `json:"alias"`
	Alia  []string `json:"alia"`
}

type neteaseSongRow struct {
	ID   int64              `json:"id"`
	Name string             `json:"name"`
	TNS  []string           `json:"tns"`
	Ar   []neteaseArtistRow `json:"ar"`
}

// neteaseSearch — 제목으로 곡을 찾는다.
func neteaseSearch(ctx context.Context, cl *http.Client, term string, limit int) ([]neteaseSongRow, error) {
	if cl == nil {
		cl = &http.Client{Timeout: 12 * time.Second}
	}
	q := url.Values{}
	q.Set("s", term)
	q.Set("type", "1")
	q.Set("limit", fmt.Sprintf("%d", limit))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, neteaseEndpoint+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", neteaseUA)
	req.Header.Set("Referer", "https://music.163.com/")
	resp, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("netease: status %d", resp.StatusCode)
	}
	var body struct {
		Result struct {
			Songs []neteaseSongRow `json:"songs"`
		} `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
		return nil, err
	}
	return body.Result.Songs, nil
}

var (
	neteaseParenRE  = regexp.MustCompile(`\s*[\(（][^)）]*[\)）]`)
	neteaseHanRE    = regexp.MustCompile(`[\p{Han}]`)
	neteaseHangulRE = regexp.MustCompile(`[\p{Hangul}]`)
)

// neteaseTitleEq — 스토어 곡명이 우리 제목과 같은가. 괄호 주석은 떼고 본다
// (「댕댕 (dangdang)」·「나 같은건 없는건가요」).
func neteaseTitleEq(ko, name string) bool {
	a, b := itunesNormTitle(ko), itunesNormTitle(name)
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	return itunesNormTitle(neteaseParenRE.ReplaceAllString(name, "")) == a
}

// neteaseZhTitle — 등록된 번역제목 중 **중국어인 것**을 고른다. 없거나 서로 다른 제목이
// 둘 이상이면 "" — 어느 쪽이 그 곡의 제목인지 말할 수 없다.
//
// 「一杯的回忆 (原唱 : 李长熙)」 같은 주석은 떼고, 「作者未定(Inst.)」 처럼 같은 제목의
// 변형만 있으면 한 제목으로 본다.
func neteaseZhTitle(tns []string) string {
	seen := map[string]string{}
	for _, t := range tns {
		base := strings.TrimSpace(neteaseParenRE.ReplaceAllString(strings.TrimSpace(t), ""))
		if base == "" || len([]rune(base)) > 40 {
			continue
		}
		if !neteaseHanRE.MatchString(base) || neteaseHangulRE.MatchString(base) {
			continue // 영문 번역제목(Ghosting·Nice Day)은 zh 가 아니다
		}
		seen[itunesNormTitle(base)] = base
	}
	if len(seen) != 1 {
		return ""
	}
	for _, v := range seen {
		return v
	}
	return ""
}

// neteaseArtistOK — 이 곡의 아티스트가 우리가 아는 그 가수인가.
func neteaseArtistOK(row neteaseSongRow, artists []itunesArtist) bool {
	for _, a := range row.Ar {
		for _, nm := range append([]string{a.Name}, append(append(append([]string{}, a.TNS...), a.Alias...), a.Alia...)...) {
			if nm == "" {
				continue
			}
			for _, ours := range artists {
				if ours.matches(nm) {
					return true
				}
			}
		}
	}
	return false
}

// neteasePick — 관문 셋을 통과한 곡 하나. 없으면 사유를 돌려준다.
func neteasePick(ko string, rows []neteaseSongRow, artists []itunesArtist) (*neteaseSongRow, string, string) {
	matched := false
	artistOK := false
	for i := range rows {
		r := &rows[i]
		if !neteaseTitleEq(ko, r.Name) {
			continue
		}
		matched = true
		if !neteaseArtistOK(*r, artists) {
			continue
		}
		artistOK = true
		if zh := neteaseZhTitle(r.TNS); zh != "" {
			return r, zh, "applied"
		}
	}
	switch {
	case artistOK:
		return nil, "", "중국어 제목 없음"
	case matched:
		return nil, "", "가수 불일치"
	default:
		return nil, "", "no_match"
	}
}

// DrainNetEaseZh — 곡의 zh 빈칸·기계값을 등록된 중국어 제목으로 채운다.
func DrainNetEaseZh(ctx context.Context, pool *pgxpool.Pool, limit int, dry bool) (applied, checked int) {
	if pool == nil || limit <= 0 {
		return 0, 0
	}
	rows, err := pool.Query(ctx, `
WITH rq AS (
  SELECT term_ko, count(*) n FROM kwave_kdb_request_terms
   WHERE created_at > now() - interval '14 days' AND origin IN ('prepare','lookup') GROUP BY 1)
SELECT e.id::text, e.canonical_ko, COALESCE(e.canonical_zh,''), COALESCE(e.canonical_zh_source,'')
  FROM kwave_entities e
  LEFT JOIN rq ON rq.term_ko = e.canonical_ko
 WHERE e.status='active' AND e.entity_type='song_album' AND e.operator_locked=false
   AND char_length(e.canonical_ko) BETWEEN 2 AND 60
   AND (COALESCE(e.canonical_zh,'')='' OR COALESCE(e.canonical_zh_source,'') = ANY($2))
   AND NOT EXISTS(SELECT 1 FROM kwave_kdb_enrich_attempts a
                  WHERE a.entity_id=e.id AND a.field='netease-zh'
                    AND a.last_attempt_at > now() - interval '30 days')
 ORDER BY COALESCE(rq.n,0) DESC, e.updated_at DESC
 LIMIT $1`, limit, MachineFilledSourcesWeakerThan(SourceNetEaseMusic))
	if err != nil {
		return 0, 0
	}
	type item struct{ id, ko, zh, zhSrc string }
	var items []item
	for rows.Next() {
		var it item
		if rows.Scan(&it.id, &it.ko, &it.zh, &it.zhSrc) == nil {
			items = append(items, it)
		}
	}
	rows.Close()

	reasons := map[string]int{}
	cl := &http.Client{Timeout: 12 * time.Second}
	for _, it := range items {
		if ctx.Err() != nil {
			break
		}
		artists := neteaseArtistsFor(ctx, pool, it.id, it.ko)
		if len(artists) == 0 {
			reasons["가수 근거 없음"]++
			recordNetEaseAttempt(ctx, pool, it.id, "no_artist")
			continue
		}
		checked++
		songs, serr := neteaseSearch(ctx, cl, it.ko, 15)
		time.Sleep(1500 * time.Millisecond) // 공개 엔드포인트 예의
		if serr != nil {
			continue // transient — 쿨다운 없이 다음 회차
		}
		hit, zh, why := neteasePick(it.ko, songs, artists)
		if hit == nil {
			reasons[why]++
			recordNetEaseAttempt(ctx, pool, it.id, why)
			continue
		}
		if !IsValidSpellingForLocale("zh", zh) {
			reasons["문자셋 거부"]++
			recordNetEaseAttempt(ctx, pool, it.id, "charset")
			continue
		}
		if dry {
			applied++
			continue
		}
		tx, txErr := pool.Begin(ctx)
		if txErr != nil {
			continue
		}
		tag, upErr := tx.Exec(ctx, `
UPDATE kwave_entities
   SET canonical_zh=$2, canonical_zh_source='netease-music', updated_at=now()
 WHERE id=$1 AND status='active' AND operator_locked=false
   AND (COALESCE(canonical_zh,'')='' OR COALESCE(canonical_zh_source,'') = ANY($3))`,
			it.id, zh, MachineFilledSourcesWeakerThan(SourceNetEaseMusic))
		if upErr != nil || tag.RowsAffected() != 1 {
			_ = tx.Rollback(ctx)
			continue
		}
		_, _ = tx.Exec(ctx, `
INSERT INTO kwave_entity_external_refs (entity_id, provider, external_id, url, confidence, raw_payload, fetched_at)
VALUES ($1,'netease',$2,$3,0.78,$4,now())
ON CONFLICT DO NOTHING`, it.id, fmt.Sprintf("%d", hit.ID),
			fmt.Sprintf("https://music.163.com/song?id=%d", hit.ID),
			fmt.Sprintf(`{"track":%q,"zh":%q}`, hit.Name, zh))
		if tx.Commit(ctx) != nil {
			continue
		}
		applied++
		recordNetEaseAttempt(ctx, pool, it.id, "applied")
	}
	reasons["승급 못함"] = checked - applied
	RecordCounts(ctx, pool, "netease-zh", dry, checked, applied, reasons)
	return applied, checked
}

// neteaseArtistsFor — 이 곡의 가수 후보. ① 이미 가진 음원 앵커의 아티스트
// ② 같은 기사 요청에 함께 들어온 active 인물·그룹(iTunes 원제 경로와 같은 근거).
func neteaseArtistsFor(ctx context.Context, pool *pgxpool.Pool, id, ko string) []itunesArtist {
	var names []string
	rows, err := pool.Query(ctx, `
SELECT DISTINCT x.a FROM kwave_entity_external_refs r,
  LATERAL (SELECT COALESCE(r.raw_payload->>'artist', r.raw_payload->>'scoped_artist') a) x
 WHERE r.entity_id=$1::uuid AND COALESCE(x.a,'') <> ''`, id)
	if err == nil {
		for rows.Next() {
			var a string
			if rows.Scan(&a) == nil {
				names = append(names, a)
			}
		}
		rows.Close()
	}
	var co []string
	rows, err = pool.Query(ctx, `
SELECT DISTINCT r2.term_ko
  FROM kwave_kdb_request_terms r1
  JOIN kwave_kdb_request_terms r2
    ON r2.request_group = r1.request_group AND r2.term_ko <> $1
   AND r2.term_type IN ('person','group')
 WHERE r1.term_ko = $1 AND r1.created_at > now() - interval '60 days'
 LIMIT 12`, ko)
	if err == nil {
		for rows.Next() {
			var a string
			if rows.Scan(&a) == nil {
				co = append(co, a)
			}
		}
		rows.Close()
	}
	out := coMentionActiveArtists(ctx, pool, strings.Join(co, " "), ko)
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, itunesArtist{ko: n, names: []string{itunesNormTitle(n)}})
		}
	}
	return out
}

func recordNetEaseAttempt(ctx context.Context, pool *pgxpool.Pool, id, outcome string) {
	_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_enrich_attempts (entity_id, field, attempts, last_attempt_at, last_source)
VALUES ($1,'netease-zh',1,now(),$2)
ON CONFLICT (entity_id, field) DO UPDATE
SET attempts=kwave_kdb_enrich_attempts.attempts+1, last_attempt_at=now(), last_source=EXCLUDED.last_source`, id, outcome)
}
