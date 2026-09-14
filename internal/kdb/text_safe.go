package kdb

import (
	"strings"
	"unicode/utf8"
)

// TruncateSafe — DB 에 넣을 문자열을 **UTF-8 로 유효하게** 만들고 글자 경계에서 자른다.
//
// `s[:n]` 은 바이트 단위로 자른다. 한글은 UTF-8 에서 3바이트라 경계가 글자 한가운데
// 떨어지면 잘린 조각(0xEB… 같은 선행 바이트)만 남고, Postgres 는 그 값을
// `invalid byte sequence for encoding "UTF8"` 로 거부해 **문장 전체가 실패**한다.
// 운영 DB 로그에서 실제로 발견했다(kwave_kdb_api_requests INSERT).
//
// 이 함수가 없으면 각 호출부가 같은 실수를 반복한다. 잘못된 바이트는 버리고
// (외부에서 온 깨진 인코딩도 여기서 걸러진다), 바이트 상한은 지키되 마지막 온전한
// 글자까지만 남긴다.
func TruncateSafe(s string, maxBytes int) string {
	s = strings.ToValidUTF8(s, "")
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
