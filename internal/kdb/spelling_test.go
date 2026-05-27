package kdb

import "testing"

func TestNormalizeSpelling(t *testing.T) {
	cases := []struct{ in, want string }{
		// 운영자 정공법 — hyphen 변종 모두 공백 normalize
		{"Park Shin-hye", "park shin hye"},
		{"Park Shin Hye", "park shin hye"},
		{"Park Shin‐hye", "park shin hye"}, // U+2010
		{"Park Shin—hye", "park shin hye"}, // em dash
		// 일본어 중점
		{"パク・シネ", "パク シネ"},
		{"パクシネ", "パクシネ"},
		// 한자
		{"朴信惠", "朴信惠"},
		// trim + 다중 공백
		{"  Park   Shin Hye  ", "park shin hye"},
		// 빈 입력
		{"", ""},
		{"   ", ""},
		// 대소문자
		{"JIMIN", "jimin"},
		{"BTS", "bts"},
	}
	for _, c := range cases {
		got := NormalizeSpelling(c.in)
		if got != c.want {
			t.Errorf("NormalizeSpelling(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
