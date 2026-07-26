package enrich

// mt_translit_fill.go — 오너 지시 2026-07-21: "직역은 버리고 구글번역으로 (음차) 채움".
// ja/zh 빈칸을 구글번역으로 돌린 뒤 gemma 음차/직역 게이트로 거른다: 음차(소리옮김)만
// 채우고 직역(뜻옮김)·깨짐은 버린다. 라이브 실증 근거: ko→zh 는 고유명사를 뜻으로 직역
// (롱샷→远距离拍摄), ko→ja 는 외래어/인명만 음차되고 순한국어 제목은 직역(놀면뭐하니→
// 遊ぶと…). 게이트로 직역을 버려 "틀린값보다 빈칸" 을 지킨다. source=gtranslate(prio8,
// machine-translation) — 상위 공식소스가 자동 업그레이드, verified_only 제외(applyEmptyOnly).

import (
	"context"
	"encoding/json"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb"
	"github.com/rickyjoo73/kdb/internal/kdb/gemma"
)

var mtHangulRe = regexp.MustCompile(`[가-힣]`)

// mtBrokenKoRe — 번역 소스로 부적합한 오염 ko(깨진 토큰·플레이스홀더). 오너 지시
// 2026-07-26: "이상없는 것만 번역해서 채워라" — 소스가 오염이면 번역 전 스킵한다.
var mtBrokenKoRe = regexp.MustCompile(`(?i)(collapsedone|undefined|\bnull\b|�|\\u[0-9a-f]{4})`)

// titleTypes — 제목류. 오너 승인 2026-07-26: 제목은 en 과 동일하게 '뜻-번역'을 서빙값으로
// 허용한다(청혼→求婚). 게이트는 translit|literal 둘 다 채우고 bad(깨짐·무의미·미번역)만
// 버린다("이상없는 것만 채움"). 이름류(person 등)는 음차만(literal 버림) 유지 — 아래 loop 참조.
var titleTypes = map[string]bool{
	"drama": true, "movie": true, "show": true, "song_album": true,
	"event_tour": true, "agency": true, "brand_place": true,
	"channel_outlet": true, "term": true,
}

var translitSchema = []byte(`{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "kind": {"type": "string", "enum": ["translit", "literal", "bad"]},
    "reason": {"type": "string"}
  },
  "required": ["kind"]
}`)

var mtTargetLang = map[string]string{"ja": "ja", "zh": "zh-CN"}

// nameTypes — 관대 게이트를 적용할 이름 타입. person(인물명) 전용이다: 한국 인명을
// 중국어로 옮기면 한자 음차, 일본어면 가나 음차 — 정의상 '음차'이므로 "애매하면
// literal(버림)" 규칙이 유효한 인명을 오거부한다(2026-07-26 실측: 하츄핑→哈楚平 을
// 엄격 게이트가 literal 로 버림, 관대 게이트가 회수). 관대=애매하면 translit.
//
// group/character 는 제외한다: 뜻-이름이 존재하고(솔개 트리오→风筝三重奏=kite trio,
// 특수임무국→特别任务局=special task bureau) 관대 게이트가 이를 오채움했다(실측). 명백히
// 발음인 그룹명(예스위아→耶斯维亚)은 엄격 게이트도 그대로 통과하므로 손실 없다. 제목
// 타입(drama/movie/show/song_album/event_tour 등)은 엄격 유지 — "빈칸>틀린값".
var nameTypes = map[string]bool{"person": true}

// judgeTranslit — gemma 판정: 기계번역 결과가 원어의 음차(소리옮김)인지 직역(뜻옮김)인지.
// 반환 kind: translit(음차, 채움가능) | literal(직역, 버림) | bad(깨짐/무의미, 버림).
// 실패/미설정 시 ("literal","") — 보수적으로 버림(오거부 방향이 아니라 오염방지 방향).
// entityType 이 이름 타입이면 관대 게이트(애매하면 translit) — 이름은 음차가 기본.
func judgeTranslit(ctx context.Context, ko, mt, locale, entityType string) (kind, reason string) {
	locName := map[string]string{"ja": "일본어", "zh": "중국어(간체)"}[locale]
	isName := nameTypes[entityType]
	var b strings.Builder
	b.WriteString("당신은 한국 고유명사(인물·그룹·작품·브랜드)의 현지표기 검수관입니다.\n")
	b.WriteString("아래 기계번역이 원어의 '음차'인지 '직역'인지 판정하세요.\n\n")
	if isName {
		b.WriteString("※ 이 항목은 **인물 이름**입니다. 한국 인명의 현지표기는 '음차'가 기본입니다.\n\n")
	}
	b.WriteString("원어(한국어): " + ko + "\n")
	b.WriteString("기계번역(" + locName + "): " + mt + "\n\n")
	b.WriteString("정의:\n")
	b.WriteString("- translit(음차) = 원어의 '소리'를 옮긴 표기. 고유명사 현지표기로 사용 가능.\n")
	b.WriteString("  예: 롱샷→ロングショット, 김현민→キム・ヒョンミン, 멜론티켓→メロンチケット.\n")
	b.WriteString("- literal(직역) = 원어의 '뜻'을 번역한 것. 고유명사 표기로 부적합(틀린 값).\n")
	b.WriteString("  예: 롱샷→远距离拍摄(원거리촬영), 멜론티켓→蜜瓜票(멜론표), 놀면 뭐하니→遊ぶと何してるの.\n")
	b.WriteString("- bad = 깨짐·무의미·원어 그대로(미번역).\n\n")
	if isName {
		b.WriteString("규칙: 이름은 음차가 기본이다. 한국 인명/그룹명을 한자·가나로 옮긴 것은 translit.\n")
		b.WriteString("명백히 '뜻'으로 번역됐거나(별명의 의미 등) 깨진 경우만 literal/bad, **애매하면 translit**.\n")
	} else {
		b.WriteString("규칙: 발음이 원어와 대응하면 translit. 의미로 번역됐으면 literal. **애매하면 literal**(틀린 표기를 넣느니 비우는 게 낫다).\n")
	}
	b.WriteString("JSON 한 개만: {\"kind\":\"translit|literal|bad\",\"reason\":\"근거 한 줄\"}\n")

	cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	raw, err := gemma.Complete(cctx, b.String(), translitSchema)
	if err != nil {
		return "literal", ""
	}
	var v struct {
		Kind   string `json:"kind"`
		Reason string `json:"reason"`
	}
	if json.Unmarshal(raw, &v) != nil || v.Kind == "" {
		return "literal", ""
	}
	return v.Kind, strings.TrimSpace(v.Reason)
}

var titleGateSchema = []byte(`{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "ok": {"type": "boolean"},
    "reason": {"type": "string"}
  },
  "required": ["ok"]
}`)

// judgeTitleTranslation — 제목류(작품·브랜드·행사) 기계번역이 '제목으로 쓸 만한 깔끔한
// 현지 표기'인지(ok=true) 아니면 '문장·오역·깨짐'인지(ok=false) 판정한다. 오너 지시
// 2026-07-26 "이상없는 것만 번역해서 채워라". 구글번역은 제목을 문장으로 풀거나(한 페이지가
// 될 수 있게→"这样它就可以只有一页了。") 오역(펜타포트→五角大楼=펜타곤)하는 사례가 많아,
// 일반 음차게이트로는 못 거른다. 휴리스틱(문장부호 종결) + LLM 판정을 이중으로 건다.
func judgeTitleTranslation(ctx context.Context, ko, mt, locale string) (ok bool, reason string) {
	t := strings.TrimSpace(mt)
	// 휴리스틱 ①: 서술 종결부호로 끝나면 제목이 아니라 문장 → 버림.
	for _, suf := range []string{"。", "．", ".", "…", "！", "!", "？", "?"} {
		if strings.HasSuffix(t, suf) {
			return false, "문장 종결부호(제목 아님)"
		}
	}
	// 휴리스틱 ②: 원어 대비 과도하게 길면(문장으로 풀림) 의심 → 버림.
	//   ko 를 문자수로 재고, 번역이 ko 의 3배+ 이며 12자 초과면 문장 가능성 큼.
	if rc, tc := len([]rune(ko)), len([]rune(t)); tc > 12 && tc >= rc*3 {
		return false, "원어 대비 과장(문장 의심)"
	}
	locName := map[string]string{"ja": "일본어", "zh": "중국어(간체)"}[locale]
	var b strings.Builder
	b.WriteString("당신은 한국 작품/브랜드/행사 '제목'의 현지표기 검수관입니다.\n")
	b.WriteString("아래 기계번역이 **제목으로 그대로 쓸 수 있는 깔끔한 표기**인지 판정하세요.\n\n")
	b.WriteString("원어(한국어 제목): " + ko + "\n")
	b.WriteString("기계번역(" + locName + "): " + mt + "\n\n")
	b.WriteString("ok=true (채움): 원어 제목의 뜻/발음을 옮긴 자연스러운 현지 제목 표기.\n")
	b.WriteString("  예: 청혼→求婚, 펜트하우스2→顶层公寓2, 포도밭→葡萄园, 아워 스토리→我们的故事.\n")
	b.WriteString("ok=false (버림): 다음 중 하나라도 해당하면 false.\n")
	b.WriteString("  - 문장으로 풀림(서술문·설명문): 한 페이지가 될 수 있게→\"这样它就可以只有一页了\".\n")
	b.WriteString("  - 오역(엉뚱한 뜻): 펜타포트→五角大楼(펜타곤 건물), 순이엔티→太阳娱乐(순이≠태양).\n")
	b.WriteString("  - 깨짐·무의미·원어 그대로(미번역)·번역기 오류.\n")
	b.WriteString("**애매하면 false**(틀린 제목을 넣느니 비우는 게 낫다).\n")
	b.WriteString("JSON 한 개만: {\"ok\":true|false,\"reason\":\"근거 한 줄\"}\n")

	cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	raw, err := gemma.Complete(cctx, b.String(), titleGateSchema)
	if err != nil {
		return false, "" // 게이트 실패 → 보수적 버림
	}
	var v struct {
		OK     bool   `json:"ok"`
		Reason string `json:"reason"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return false, ""
	}
	return v.OK, strings.TrimSpace(v.Reason)
}

// MTTranslitFill — active 엔티티의 locale(ja|zh) 빈칸을 구글번역→음차게이트로 채운다.
// dry=true 는 판정·로그만(DB 미변경 — 테스트/미리보기). 반환 (filled, discarded, processed).
func (o *Orchestrator) MTTranslitFill(ctx context.Context, locale string, limit int, dry bool) (filled, discarded, processed int) {
	target, ok := mtTargetLang[locale]
	if !ok {
		log.Printf("kdb.mt-translit: 미지원 locale=%q (ja|zh 만)", locale)
		return 0, 0, 0
	}
	if !gemma.Configured() {
		log.Printf("kdb.mt-translit: gemma 미설정 — 중단(음차게이트 불가)")
		return 0, 0, 0
	}
	g := kdb.NewGTranslator(o.Pool)
	if g == nil {
		log.Printf("kdb.mt-translit: KDB_GTRANSLATE_KEY 미설정 — 중단")
		return 0, 0, 0
	}
	col := "canonical_" + locale
	// 시도추적 키 — 구글이 못 옮기는 고유명사·직역으로 버려진 항목이 매 회차 updated_at DESC
	// 리스트 머리에 재등장해 드레인이 수렴 못 하던 버그(2026-07-26) 수정. 버림 시 이 field 에
	// 쿨다운(30d)+exhausted(3회) 를 찍어 재선택에서 제외 → 드레인이 백로그 전체를 관통한다.
	attemptField := "mt-fill:" + locale
	rows, err := o.Pool.Query(ctx, `
SELECT id::text FROM kwave_entities e
 WHERE status='active' AND operator_locked=false
   AND COALESCE(`+col+`,'')='' AND canonical_ko ~ '[가-힣]'
   AND entity_type NOT IN ('unknown','term')
   AND NOT EXISTS (SELECT 1 FROM kwave_kdb_enrich_attempts g
        WHERE g.entity_id=e.id AND g.field=$2
          AND (g.exhausted OR g.last_attempt_at > now()-interval '30 days'))
 ORDER BY updated_at DESC
 LIMIT $1`, limit, attemptField)
	if err != nil {
		log.Printf("kdb.mt-translit: query: %v", err)
		return 0, 0, 0
	}
	var ids []string
	for rows.Next() {
		var s string
		if rows.Scan(&s) == nil {
			ids = append(ids, s)
		}
	}
	rows.Close()
	log.Printf("kdb.mt-translit: %s 음차채움 시작 (%d건, dry=%v)", locale, len(ids), dry)

	for _, idStr := range ids {
		if ctx.Err() != nil {
			break
		}
		uid, perr := uuid.Parse(idStr)
		if perr != nil {
			continue
		}
		snap, serr := loadSnapshot(ctx, o.Pool, uid)
		if serr != nil || snap == nil || snap.Values[locale] != "" {
			continue
		}
		processed++
		// 소스 오염 사전차단 — 깨진 ko 는 번역하지 않는다(오너 "이상없는 것만"). 쿨다운 마킹해
		// 재선택 제외.
		if mtBrokenKoRe.MatchString(snap.Ko) {
			discarded++
			if !dry {
				markMTAttempt(ctx, o.Pool, idStr, attemptField, "broken-source")
			}
			continue
		}
		isTitle := titleTypes[snap.EntityType]
		mt, terr := g.TranslateRawTo(ctx, snap.Ko, target)
		if terr != nil {
			continue // 일시 장애 — 다음 회차
		}
		if mt == "" { // 미번역(구글이 모르는 고유명사) — 버림. 결정적 실패라 쿨다운.
			discarded++
			if !dry {
				markMTAttempt(ctx, o.Pool, idStr, attemptField, "untranslatable")
			}
			continue
		}
		// 수용 규칙(타입별):
		//   이름류 → judgeTranslit: 음차(translit)만 채움. 직역/깨짐 버림.
		//   제목류 → judgeTitleTranslation: 제목으로 쓸 만한 깔끔한 번역(ok)만 채움. 문장·오역·
		//            깨짐(bad) 버림. 구글이 제목을 문장으로 풀거나(한 페이지가 될 수 있게→"…了。")
		//            오역(펜타포트→五角大楼=펜타곤)하는 걸 걸러 "이상없는 것만" 채운다.
		var accept bool
		var kind, reason, mode string
		if isTitle {
			mode = "뜻번역"
			accept, reason = judgeTitleTranslation(ctx, snap.Ko, mt, locale)
			if !accept {
				kind = "bad-title"
			}
		} else {
			mode = "음차"
			kind, reason = judgeTranslit(ctx, snap.Ko, mt, locale, snap.EntityType)
			accept = kind == "translit"
		}
		if !accept { // 버림 — 동일 입력→동일 판정이라 쿨다운.
			discarded++
			if dry {
				log.Printf("kdb.mt-translit[dry]: 버림(%s) %q → %q (%s)", kind, snap.Ko, mt, reason)
			} else {
				markMTAttempt(ctx, o.Pool, idStr, attemptField, kind)
			}
			continue
		}
		if dry {
			log.Printf("kdb.mt-translit[dry]: 채움후보(%s) %q → %q", mode, snap.Ko, mt)
			filled++
			continue
		}
		applied, _ := o.applyEmptyOnly(ctx, snap, map[string][]string{locale: {mt}}, kdb.SourceGTranslate)
		if len(applied) > 0 {
			filled++
			log.Printf("kdb.mt-translit: %q → %s=%q (%s)", snap.Ko, locale, mt, mode)
		} else {
			discarded++ // 문자셋/suppress 가드 기각 또는 동시 채움 — 가드는 결정적이라 쿨다운.
			markMTAttempt(ctx, o.Pool, idStr, attemptField, "guard-reject")
		}
	}
	log.Printf("kdb.mt-translit: %s 완료 filled=%d discarded=%d /%d (dry=%v)", locale, filled, discarded, processed, dry)
	return filled, discarded, processed
}

// markMTAttempt — MT 음차채움에서 버려진 항목을 (entity, field='mt-fill:<locale>') 로 기록해
// 재선택에서 제외한다. 30d 쿨다운(선택 쿼리) + 3회 후 exhausted(영구 제외). 결정적 실패
// (미번역·직역·가드기각)만 마킹 — 일시 장애(번역 API 오류)는 호출측이 마킹 없이 다음 회차로
// 넘긴다. cand-evidence 의 markCandEvidenceInsufficient 와 동일 패턴.
func markMTAttempt(ctx context.Context, pool *pgxpool.Pool, entityID, field, kind string) {
	var attempts int
	err := pool.QueryRow(ctx, `
INSERT INTO kwave_kdb_enrich_attempts (entity_id, field, attempts, last_attempt_at, last_source)
VALUES ($1,$2,1,now(),$3)
ON CONFLICT (entity_id, field) DO UPDATE
SET attempts=kwave_kdb_enrich_attempts.attempts+1, last_attempt_at=now(),
    last_source=EXCLUDED.last_source
RETURNING attempts`, entityID, field, kind).Scan(&attempts)
	if err == nil && attempts >= 3 {
		_, _ = pool.Exec(ctx, `UPDATE kwave_kdb_enrich_attempts SET exhausted=true
			WHERE entity_id=$1 AND field=$2`, entityID, field)
	}
}
