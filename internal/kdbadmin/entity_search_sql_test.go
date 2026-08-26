package kdbadmin

import "testing"

// renumberFromOffset 은 list 쿼리의 WHERE 를 count 쿼리로 재사용할 때 자리표시자를
// 당긴다. 예전 구현은 순차 치환이라 자기가 만든 번호를 다시 집었고, args 가 5개가
// 되는 순간(검색어 + 정규화키 + 유형필터) 유형필터가 검색어 자리로 뭉개졌다.
func TestRenumberFromOffset(t *testing.T) {
	cases := []struct {
		name, in, want string
		offset         int
	}{
		{"검색어+유형(4 args)", "WHERE q=$3 AND type=$4", "WHERE q=$1 AND type=$2", 2},
		{"검색어+정규화키+유형(5 args)", "WHERE q=$3 AND nph=$4 AND type=$5", "WHERE q=$1 AND nph=$2 AND type=$3", 2},
		{"두 자리 번호", "a=$3 b=$10 c=$12", "a=$1 b=$8 c=$10", 2},
		{"offset 이하는 그대로", "a=$1 b=$2 c=$3", "a=$1 b=$2 c=$1", 2},
		{"달러인용은 건드리지 않는다", "x=$$abc$$ AND y=$3", "x=$$abc$$ AND y=$1", 2},
	}
	for _, c := range cases {
		if got := renumberFromOffset(c.in, c.offset); got != c.want {
			t.Errorf("%s: renumberFromOffset(%q,%d) = %q, want %q", c.name, c.in, c.offset, got, c.want)
		}
	}
}

// normSearchSQL 은 빈 정규화키를 반드시 막아야 한다 — 안 막으면 '%%' 가 되어
// 검색어와 무관하게 전건이 걸린다.
func TestNormSearchSQLGuardsEmptyKey(t *testing.T) {
	sql := normSearchSQL("$4", []string{"canonical_ko"}, "aliases_ko || aliases_en")
	for _, want := range []string{"$4 <> ''", "regexp_replace", "unnest(aliases_ko || aliases_en)"} {
		if !contains(sql, want) {
			t.Errorf("normSearchSQL 에 %q 가 없다: %s", want, sql)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// 정규화 부분일치는 반드시 어절 앵커와 짝이어야 한다. 앵커가 빠지면 어절 경계를
// 넘어 우연히 붙은 것까지 걸린다("더시즌" 이 "해피투게더 시즌3" 을 집었다).
// 그리고 값싼 정규화 필터가 AND 왼쪽에 와야 한다 — 앵커를 전건에 돌리면 859ms 다.
func TestNormSearchSQLAnchorsToTokenStart(t *testing.T) {
	sql := normSearchSQL("$4", []string{"canonical_ko"}, "aliases_ko")
	for _, want := range []string{"regexp_split_to_array", "generate_subscripts", "array_to_string"} {
		if !contains(sql, want) {
			t.Fatalf("어절 앵커가 빠졌다 (%q 없음): %s", want, sql)
		}
	}
	cheap := indexOf(sql, "regexp_replace")
	anchor := indexOf(sql, "regexp_split_to_array")
	if cheap < 0 || anchor < 0 || cheap > anchor {
		t.Errorf("값싼 정규화 필터가 앵커보다 앞에 와야 한다: %s", sql)
	}
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
