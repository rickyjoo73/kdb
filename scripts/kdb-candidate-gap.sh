#!/usr/bin/env bash
# kdb-candidate-gap.sh — **후보가 왜 멈췄는지**를 SQL 없이 본다. 읽기 전용.
#
# ★왜 만들었나 (2026-09-20). 발굴 결말의 60%가 candidate 에서 멈춘다. 그 이유를
#   알아내는 데 매번 열 몇 개의 질의를 새로 짜야 했다 — 그래서 매 회차 같은 자리를
#   다시 팠다. 판단에 필요한 것만 한 화면에 모은다.
#
# ★이 스크립트가 답하는 것:
#   ① 후보가 근거를 갖고 있나 (출처 URL·검증등급·confidence)
#   ② 시도라도 했나 (enrich_attempts 기록)
#   ③ 표기 채움 레인이 후보를 보기는 하나 (active vs candidate 대상 수)
#
# ★2026-09-20 실측으로 드러난 교착:
#     후보 976건 — 시도기록 976/976 · 출처URL 1 · 검증등급 0 · confidence 전부 0.40
#     레인 선정 조건은 전부 status='active'
#     song_album 중 ja 빈칸: active 449 · candidate 263
#   후보는 «근거가 없어서» 후보인데, 근거를 만들 레인이 후보를 안 본다.
#
# 사용: 서버23 에서  bash scripts/kdb-candidate-gap.sh  [일수(기본 7)]
set -euo pipefail
DAYS="${1:-7}"
Q() { docker exec -i kdb-db psql -qAtX -F ' | ' -U kdb -d kdb -c "$1"; }

echo "=== 최근 ${DAYS}일 신규 대상의 상태별 근거 보유 ==="
Q "SELECT status,
          count(*) 건수,
          round(avg(confidence),2) 평균conf,
          count(*) FILTER (WHERE coalesce(verification_tier,'')<>'') 검증등급,
          count(*) FILTER (WHERE array_length(source_urls,1) > 0) 출처URL,
          count(*) FILTER (WHERE EXISTS(SELECT 1 FROM kwave_kdb_enrich_attempts a WHERE a.entity_id=e.id)) 시도기록
     FROM kwave_entities e
    WHERE created_at > now() - interval '${DAYS} days'
    GROUP BY 1 ORDER BY 2 DESC;"

echo
echo "=== 멈춘 후보의 유형 (어느 영역에 권위 소스가 없는지) ==="
Q "SELECT entity_type::text 유형, count(*) 후보
     FROM kwave_entities
    WHERE status='candidate' AND created_at > now() - interval '${DAYS} days'
    GROUP BY 1 ORDER BY 2 DESC LIMIT 12;"

echo
echo "=== 레인이 후보를 보는가 — locale 빈칸 대상 수 (active vs candidate) ==="
for t in song_album person show event_tour organization company; do
  Q "SELECT '${t}' 유형,
            count(*) FILTER (WHERE status='active') active,
            count(*) FILTER (WHERE status='candidate') candidate
       FROM kwave_entities
      WHERE entity_type='${t}' AND coalesce(canonical_ja,'')='';"
done

echo
echo "※ candidate 열이 크면 그 유형은 레인 밖에 있다 — 레인 선정 조건이 status='active' 다."
