package kdbadmin

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// --- 동명이인 (homonym) 그룹 리뷰 ---------------------------------------
//
// 같은 canonical_ko 를 공유하는 서로 다른 실존 인물(예: 윤성호 감독 vs 가수)을
// agency/works/role/birth 와 함께 나란히 보여주고, 운영자가 disambig 라벨을
// 지정하거나 reject(잘못된 분리)할 수 있게 한다. 자동 merge/split 은 하지 않음.
//
// ★정체성 규칙: **한 사람 = 하나의 ID** 다. ID 가 갈리는 이유는 "직업이 달라서"가
// 아니라 "다른 사람이어서"다. 한 사람이 배우·가수·MC 를 겸하면 role 행이 여러
// 개일 뿐 ID 는 하나이며, 겸업을 근거로 ID 를 쪼개면 같은 사람이 두 UUID 를 갖게
// 돼 소비자 쪽 저장값이 갈린다(I01: 직업은 UUID 유일 키가 아니다 / I09: 직업
// 추가는 기존 UUID 의 변경 / 구조 §2 "복수 직업과 UUID별 값 소유").
// 그래서 이 화면의 직업 필터는 primary_role 하나가 아니라 그 사람의 **모든**
// 직업을 본다. 동명이인 구분은 직업이 아니라 소속사·생년·대표작·표기로 한다.

// homonymMember — 한 그룹 내 entity 1건 (구분 신호 포함).
type homonymMember struct {
	ID            uuid.UUID
	EntityType    string
	Disambig      string
	PrimaryRole   string
	Agency        string
	BirthYear     int
	NotableWorks  []string
	Confidence    float64
	Status        string
	NeedsDisambig bool
	Global        bool     // foreign-locale 표기 보유 여부
	Roles         []string // 이 한 사람이 가진 직업 전부. 여러 개인 것이 정상이다.
	Spellings     []localeSpelling // 후보별 목표 locale 표기 — 동명이인 판별의 실제 근거
	UpdatedAt     time.Time
}



// homonymGroup — 같은 canonical_ko 를 공유하는 entity 묶음.
type homonymGroup struct {
	Ko      string
	Count   int
	Members []homonymMember
}

func (s *Server) entityHomonyms(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// 직업(역할) 필터. 후보를 고르려면 둘 다 보여야 하므로 *구성원*이 아니라 *그룹*을
	// 거른다 — 해당 직업이 한 명이라도 있는 그룹만 남기고, 그 안의 후보는 전원 보여준다.
	// 한쪽만 남기면 비교 자체가 불가능해져 "고르기" 화면의 목적이 사라진다.
	role := strings.TrimSpace(r.URL.Query().Get("role"))
	if len([]rune(role)) > 40 {
		http.Error(w, "역할 조건을 확인하세요.", 400)
		return
	}
	// 공통 person_roles 는 P1.02 구조에만 있다. 없으면 legacy 의 primary/secondary 만 본다
	// — 코드가 구조보다 먼저 배포돼도 목록이 죽지 않아야 한다.
	var multiRole bool
	if err := s.pool.QueryRow(ctx, `SELECT to_regclass('public.kentity_person_roles') IS NOT NULL`).Scan(&multiRole); err != nil {
		multiRole = false
	}
	roleClause := ` AND ($1 = '' OR EXISTS (
   SELECT 1 FROM kwave_entities g
     JOIN kwave_entity_person_details gd ON gd.entity_id = g.id
    WHERE g.canonical_ko = e.canonical_ko AND g.status <> 'rejected'
      AND ($1 = gd.primary_role::text OR $1 = ANY(COALESCE(gd.secondary_roles::text[],'{}'::text[])))))`
	if multiRole {
		roleClause = ` AND ($1 = '' OR EXISTS (
   SELECT 1 FROM kwave_entities g
    WHERE g.canonical_ko = e.canonical_ko AND g.status <> 'rejected'
      AND (EXISTS (SELECT 1 FROM kwave_entity_person_details gd WHERE gd.entity_id = g.id
                    AND ($1 = gd.primary_role::text OR $1 = ANY(COALESCE(gd.secondary_roles::text[],'{}'::text[]))))
        OR EXISTS (SELECT 1 FROM kentity_person_roles pr WHERE pr.entity_id = g.id
                    AND pr.role_code = $1 AND pr.status <> 'withdrawn'))))`
	}

	// canonical_ko 가 2건 이상인 그룹 (= 진짜 동명이인 공존). UNIQUE 제거 후 발생.
	rows, err := s.pool.Query(ctx, `
SELECT e.canonical_ko, e.id, e.entity_type::text, COALESCE(e.disambig,''),
       COALESCE(d.primary_role::text,''), COALESCE(d.agency,''),
       COALESCE(d.birth_year,0), COALESCE(d.notable_works,'{}'::text[]),
       e.confidence, e.status, e.needs_disambig, e.updated_at,
       (COALESCE(e.canonical_en,'')<>'' OR COALESCE(e.canonical_ja,'')<>''
        OR COALESCE(e.canonical_vi,'')<>'' OR COALESCE(e.canonical_id,'')<>''
        OR COALESCE(e.canonical_es,'')<>'' OR COALESCE(e.canonical_pt_br,'')<>''
        OR COALESCE(e.canonical_zh_hant,'')<>'') AS global,
       COALESCE(e.canonical_en,''), COALESCE(e.canonical_ja,''), COALESCE(e.canonical_zh_hant,''),
       COALESCE(d.secondary_roles::text[],'{}'::text[])
  FROM kwave_entities e
  LEFT JOIN kwave_entity_person_details d ON d.entity_id = e.id
 WHERE e.canonical_ko IN (
   SELECT canonical_ko FROM kwave_entities
   WHERE status <> 'rejected'
   GROUP BY canonical_ko HAVING COUNT(*) > 1
 )
 AND e.status <> 'rejected'`+roleClause+`
 ORDER BY e.canonical_ko, e.confidence DESC`, role)
	if err != nil {
		s.renderError(w, r, "homonyms list", err)
		return
	}
	defer rows.Close()

	groupMap := map[string]*homonymGroup{}
	order := []string{}
	for rows.Next() {
		var ko, en, ja, hant string
		var secondary []string
		var m homonymMember
		if err := rows.Scan(&ko, &m.ID, &m.EntityType, &m.Disambig, &m.PrimaryRole,
			&m.Agency, &m.BirthYear, &m.NotableWorks, &m.Confidence, &m.Status,
			&m.NeedsDisambig, &m.UpdatedAt, &m.Global, &en, &ja, &hant, &secondary); err != nil {
			continue
		}
		m.Spellings = []localeSpelling{{Label: "EN", Code: "en", Value: en}, {Label: "JA", Code: "ja", Value: ja}, {Label: "ZH-Hant", Code: "zh-Hant", Value: hant}}
		m.Roles = mergeRoles(m.PrimaryRole, secondary)
		g, ok := groupMap[ko]
		if !ok {
			g = &homonymGroup{Ko: ko}
			groupMap[ko] = g
			order = append(order, ko)
		}
		g.Members = append(g.Members, m)
		g.Count++
	}
	groups := make([]homonymGroup, 0, len(order))
	for _, ko := range order {
		groups = append(groups, *groupMap[ko])
	}

	// needs_disambig 표시된 단독(아직 그룹화 안 된) entity 도 별도로 노출.
	flagged := []homonymMember{}
	var koFlag []string
	if fRows, fErr := s.pool.Query(ctx, `
SELECT e.id, e.canonical_ko, e.entity_type::text, COALESCE(e.disambig,''),
       COALESCE(d.primary_role::text,''), COALESCE(d.agency,''),
       COALESCE(d.birth_year,0), COALESCE(d.notable_works,'{}'::text[]),
       e.confidence, e.status, e.updated_at, COALESCE(d.secondary_roles::text[],'{}'::text[])
  FROM kwave_entities e
  LEFT JOIN kwave_entity_person_details d ON d.entity_id = e.id
 WHERE e.needs_disambig = true AND e.status <> 'rejected'
 ORDER BY e.updated_at DESC LIMIT 100`); fErr == nil {
		defer fRows.Close()
		for fRows.Next() {
			var m homonymMember
			var ko string
			var fSecondary []string
			if err := fRows.Scan(&m.ID, &ko, &m.EntityType, &m.Disambig, &m.PrimaryRole,
				&m.Agency, &m.BirthYear, &m.NotableWorks, &m.Confidence, &m.Status,
				&m.UpdatedAt, &fSecondary); err == nil {
				m.NeedsDisambig = true
				m.Roles = mergeRoles(m.PrimaryRole, fSecondary)
				flagged = append(flagged, m)
				koFlag = append(koFlag, ko)
			}
		}
	}

	// 공통 person_roles 로 직업을 보강한다. 한 ID 에 여러 행이 정상이며, 그 전부가
	// 같은 한 사람의 직업이다(겸업). legacy 값과 합쳐 중복만 없앤다.
	if multiRole {
		ids := []uuid.UUID{}
		for _, g := range groups {
			for _, m := range g.Members {
				ids = append(ids, m.ID)
			}
		}
		byID := map[uuid.UUID][]string{}
		if rRows, rErr := s.pool.Query(ctx, `SELECT entity_id, array_agg(DISTINCT role_code ORDER BY role_code)
  FROM kentity_person_roles WHERE entity_id = ANY($1) AND status <> 'withdrawn' GROUP BY entity_id`, ids); rErr == nil {
			for rRows.Next() {
				var id uuid.UUID
				var codes []string
				if err := rRows.Scan(&id, &codes); err == nil {
					byID[id] = codes
				}
			}
			rRows.Close()
		}
		for gi := range groups {
			for mi := range groups[gi].Members {
				m := &groups[gi].Members[mi]
				m.Roles = mergeRoles(strings.Join(m.Roles, "\x00"), byID[m.ID])
			}
		}
		for i := range flagged {
			flagged[i].Roles = mergeRoles(strings.Join(flagged[i].Roles, "\x00"), byID[flagged[i].ID])
		}
	}

	// 필터에 쓸 직업 목록은 지금 화면에 있는 그룹에서만 뽑는다 — 전체 사전을 나열하면
	// 고르는 사람이 "어떤 값이 실제로 결과를 바꾸는지" 알 수 없다.
	roleSet := map[string]bool{}
	for _, g := range groups {
		for _, m := range g.Members {
			for _, rc := range m.Roles {
				roleSet[rc] = true
			}
		}
	}
	roles := make([]string, 0, len(roleSet))
	for rc := range roleSet {
		roles = append(roles, rc)
	}
	sort.Strings(roles)

	s.render(w, r, "entity_homonyms.html", map[string]any{
		"title":   "동명 후보 고르기",
		"groups":  groups,
		"flagged": flagged,
		"koFlag":  koFlag,
		"role":    role,
		"roles":   roles,
		"flash":   r.URL.Query().Get("flash"),
		"page":    "/admin/entities/homonyms",
	})
}

// entitySetDisambig — 운영자가 entity 에 disambig 라벨 지정 + needs_disambig 해제.
func (s *Server) entitySetDisambig(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	disambig := strings.TrimSpace(r.FormValue("disambig"))
	clearFlag := r.FormValue("clear_flag") != ""
	if clearFlag {
		_, err = s.pool.Exec(ctx, `
UPDATE kwave_entities SET disambig = NULLIF($2,''), needs_disambig = false, updated_at = now()
 WHERE id = $1`, id, disambig)
	} else {
		_, err = s.pool.Exec(ctx, `
UPDATE kwave_entities SET disambig = NULLIF($2,''), updated_at = now()
 WHERE id = $1`, id, disambig)
	}
	flash := "disambig+saved"
	if err != nil {
		flash = "error"
	}
	http.Redirect(w, r, "/admin/entities/homonyms?flash="+flash, http.StatusSeeOther)
}

// mergeRoles — 한 사람의 직업을 하나로 모은다. 여러 개인 것이 정상이므로 자르지
// 않고 중복만 없앤다. 겸업을 이유로 ID 를 나누지 않기 때문에, 이 목록이 길다는
// 것은 "다른 사람이 섞였다"는 신호가 아니다.
func mergeRoles(primary string, more []string) []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			return
		}
		seen[v] = true
		out = append(out, v)
	}
	for _, v := range strings.Split(primary, "\x00") {
		add(v)
	}
	for _, v := range more {
		add(v)
	}
	sort.Strings(out)
	return out
}
