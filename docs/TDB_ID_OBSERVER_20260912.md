# TDB 원본 ID 정기 재관측

이 문서는 구현 계약이다. 실제 운영 활성화·실행 시각은 운영 인계의 최신 기록을 확인한다.
0122 스키마를 사용하며 TDB 스키마·계정·원본 데이터는 변경하지 않는다.

## 구성

- KDB 운영 계정(UID 1014)의 `scripts/observe-tdb-shadow.cjs`가 알려진 비교 ID 중
  가장 오래 관측한 최대 100개를 읽는다. 신규 TDB 전량 수집기가 아니다.
- TDB에 5초 제한 read-only 질의를 실행해 원본 활성·병합·잠금, 기존 QID 연결 존재,
  원천 레코드 삭제, 출처 live/enabled/CC0/비생성 여부, 원본 유형만 관측한다.
- 원본 이름·좌표·주소·payload·config·API 키는 선택하지 않는다.
- 대상 집합과 결과 집합이 정확히 같은 정상 응답만 제출한다. 중복/누락/대상 변경/
  timeout은 삭제 증거가 아니며 DB에 부정 관측으로 기록하지 않는다.
- CLI `tdb-shadow-observe --list`와 `tdb-shadow-observe [--apply]`를 사용한다.
  기본 dry-run, JSON 64KiB·100건·5분 내 snapshot, 전체 처리 25초 제한이다.
- `KDB_TDB_OBSERVER_ENABLED=1`, 기존 common/shadow gate가 있어야 실제 관측을 적용한다.
  앱에 Docker socket이나 TDB DB 자격 증명을 추가하지 않는다. 운영자 helper만 기존 Docker 접근을 쓴다.

## 반영 규칙

각 ID 관측은 독립 트랜잭션이다. 중간 장애로 일부만 반영됐으면 같은 집합을 재관측하여 재전송한다.
새 snapshot이 아니거나 대상 ID가 변경됐으면 거절한다. 재전송은 멱등이며 삭제·이름 승인·병합을 하지 않는다.

- 같은 정상 관측: 관측 시각만 갱신. generation·시도 횟수·연결 승인·독립 이름 결과 유지.
- 변경된 정상 관측: 기존 source fingerprint 보호대로 새 조사를 예약하고 연결을 재검토로 전환.
- 정상 조회로 확인한 사용 불가: `source_policy_blocked`, `place_inactive`, `link_missing`,
  `record_deleted`, `type_unsupported`. 결과·lease를 폐기하고 원본 연결 근거를 철회한다.
  공통 master나 TDB 원본은 삭제하지 않는다.
- 반복된 동일 사용 불가: 관측 시각만 갱신. 이력·generation을 계속 증가시키지 않는다.
- 다시 사용 가능: 새 독립 조사를 예약하되 이전 동일인/언어 승인을 자동 복구하지 않는다.
- 독립 원천 조사 24시간 경과, 또는 실패·근거 부족의 마지막 처리로부터 24시간 경과 시
  최대 4회짜리 한 세대를 재예약한다. 동일 입력의 매분 무한 재시도를 허용하지 않는다.

## 정기 실행과 확인

운영 계정 cron은 매분, `flock -n`으로 중복 실행을 막고 고정된 helper 이미지로 실행한다.
원본 DB는 read-only이며 KDB 앱 외 다른 서비스를 재기동하지 않는다.
작업 결과는 `kdb-tdb-id-observer` syslog 태그로 건수만 기록한다. 비밀값이나 원천 응답을 출력하지 않는다.

호스트 설정 파일: `/etc/cron.d/kdb-tdb-id-observer`.
잠금 파일: `/data/home2/kdb.aiinplanet.com/run/kentity-tdb-id-observer.lock`.
설치할 때 기존 파일이 있으면 덮어쓰지 말고 소유/내용을 확인한다. 적용 전 dry-run과 직접 실행을
통과하고, 적용 후 **수동 호출 없이 다음 관측 시각이 증가하는지** 확인한다.

`/admin/kentity/tdb`에는 추적 수/15분 내 관측/관측 지연/원천 보류와 가장 오래된 관측 시각을
표시한다. 기능 허용을 실제 실행 성공이나 언어 준비 완료로 표시하지 않는다.
현재 한정 shadow 집합 대상이며, 대규모 이관 시 분당 100개 상한과 15분 신선도에 맞춰
증분 cursor/outbox 또는 처리 규모를 별도로 검증해야 한다.

## 검증·복구

- 단위/DB: 정상 관측 멱등, dry-run 무변경, 부정 관측 철회/반복/복구, 오래된 입력·불완전 대상 거절,
  24시간 독립 조회 갱신 및 4회 예산, 소스 잠금, 신규 snapshot 이전 작업 fencing.
- bridge: read-only/ID-only SQL, 정확 대상 집합, timeout·누락을 삭제로 오인하지 않음, KDB UID.
- 복원한 실제 스키마의 후보·연결 승인·원본 잠금·부정 관측과 기존 trigger 회귀 검증.
- 문제 시 observer gate를 끄고 해당 cron 항목만 중지한다. 비교/audit/master/TDB 원본을 삭제하지 않는다.
  그 뒤 필요하면 writer-aware `tdb-mapping-20260912-1` 앱으로 복귀한다.

이 관측기는 실제 정체성 승격, 관광 속성 재배포 권리, 공통 소비자 전환을 대신하지 않는다.
