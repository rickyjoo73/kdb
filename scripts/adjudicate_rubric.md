# 인테이크 review 큐 판정 루브릭 (2026-07-25, 39차)

대상: KDB(한국 엔터테인먼트 고유명사 DB)에 소비자가 요청했으나 인테이크 게이트가
근거부족으로 'review' 보류한 이름들. 아직 엔티티가 없다. 너의 지식으로 판정하라.
검색 없이 지식만으로 판단한다.

## 입력 TSV 컬럼 (탭 구분)
id / entity_ko(요청 이름) / requested_type(소비자 힌트, unknown 가능) /
context_hint(기사 문맥, 대부분 빈칸) / domain(출처 매체) / precheck_reason(보류 사유)

## verdict 3종
- **approve**: 실존하는 한국 엔터테인먼트 고유명사임을 확신(K-드라마·영화·예능·곡·앨범·
  K-pop 그룹/인물·기획사·방송사·공연/투어·캐릭터 등). type 필드에 올바른 타입 명시.
  evidence 에 정체를 구체적으로(예: "2024 tvN 드라마", "아이유 2023 싱글").
  → 이 이름은 발굴 파이프라인으로 넘어가 채워진다.
- **reject**: 다음이 명백할 때만 — ①비-K(외국 인물/작품/기업, K-엔터 무관 — 예: 우리은행,
  삼성전자, 애플, 미국/일본 배우·가수·작품, 외국 스포츠팀/선수, 정치인, 기업인)
  ②일반어·문장·서술구·구절(고유명사 아님) ③깨진 토큰/자소분리 오타 ④기사 코너명·문장 일부.
  reason 필수.
- **keep**: 위 둘 다 확신 못 함 → 판정 보류(autoverify 가 근거수집 후 처리). reason 선택.

## 최상위 금칙
**오거부(실존 K-엔터를 reject) = 최악.** 조금이라도 불확실하면 keep.
- 흔한 단어 제목(행복·미쳤어·봄날 등)도 실존 K-곡/작품이면 approve.
- 모르는 이름을 아는 척 approve 하지도 마라 — 확신 없으면 keep.
- K-엔터 인물의 외국국적 멤버(뉴진스 하니/다니엘, aespa 닝닝 등)는 approve(person).
- 비-K가 명백한 것(은행·대기업·외국 유명인)만 reject. 애매한 한국어 이름은 keep.

## 유효 type
person group drama movie show song_album agency channel_outlet brand_place event_tour character term

## 출력 (JSONL, 행마다 한 줄, 다른 텍스트 금지)
{"id":"<id>","ko":"<entity_ko>","verdict":"approve|reject|keep","type":"<approve시>","evidence":"<approve시 정체>","reason":"<reject시 사유>"}
입력 전 행을 빠짐없이 출력하라(개수 일치 필수).
