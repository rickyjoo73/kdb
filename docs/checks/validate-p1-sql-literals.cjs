#!/usr/bin/env node
// P1 격리 SQL 의 UUID 리터럴이 실제로 16진수인지 본다.
//
// 왜 필요한가: 합성 픽스처를 손으로 쓰다 보면 'e0l1-…'(L), 'e0i1-…'(I), '7c00s001-…'(S)
// 처럼 UUID 모양이지만 16진수가 아닌 값이 들어간다. 눈으로는 UUID 로 보이고, 파일 전체가
// 한 트랜잭션이라 psql 은 그 한 줄에서 멈춰 **시험 결과 표 자체가 만들어지지 않는다**.
// 그러면 "시험 N/N PASS" 가 아니라 아무 결과도 없는 상태가 되는데, 실패 메시지를 끝까지
// 읽지 않으면 이걸 "아직 안 돌았다"로 오해하기 쉽다. 실제로 세 번 겪었다.
const fs = require("fs");
const path = require("path");

const files = ["docs/p1/p1_tests.sql", "docs/p1/p1_structure.sql"];
const shaped = /'([0-9a-zA-Z]{8}-[0-9a-zA-Z]{4}-[0-9a-zA-Z]{4}-[0-9a-zA-Z]{4}-[0-9a-zA-Z]{12})'/g;
const hex = /^[0-9a-f-]+$/;

let bad = 0;
for (const rel of files) {
  const p = path.resolve(process.cwd(), rel);
  if (!fs.existsSync(p)) {
    console.error(`FAIL ${rel}: 파일이 없다`);
    bad++;
    continue;
  }
  const text = fs.readFileSync(p, "utf8");
  const lines = text.split("\n");
  lines.forEach((line, i) => {
    for (const m of line.matchAll(shaped)) {
      if (!hex.test(m[1])) {
        console.error(`FAIL ${rel}:${i + 1}: '${m[1]}' 는 16진수가 아니다`);
        bad++;
      }
    }
  });
}
if (bad) process.exit(1);
console.log("PASS p1 SQL UUID 리터럴");
