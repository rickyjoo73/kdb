package homonym

import "testing"

func TestConflict(t *testing.T) {
	cases := []struct {
		name string
		a, b PersonSignals
		want bool
	}{
		{"empty signals not a conflict", PersonSignals{}, PersonSignals{}, false},
		{"different agency conflicts", PersonSignals{Agency: "YG"}, PersonSignals{Agency: "SM"}, true},
		{"same agency no conflict", PersonSignals{Agency: "YG"}, PersonSignals{Agency: "yg"}, false},
		{"one empty agency no conflict", PersonSignals{Agency: "YG"}, PersonSignals{}, false},
		{"different birth year conflicts", PersonSignals{BirthYear: 1980}, PersonSignals{BirthYear: 1990}, true},
		{"director vs singer conflicts", PersonSignals{PrimaryRole: "director"}, PersonSignals{PrimaryRole: "singer"}, true},
		{"other role does not trigger", PersonSignals{PrimaryRole: "other"}, PersonSignals{PrimaryRole: "singer"}, false},
		{"disjoint works conflict", PersonSignals{NotableWorks: []string{"Film A"}}, PersonSignals{NotableWorks: []string{"Song B"}}, true},
		{"overlapping works no conflict", PersonSignals{NotableWorks: []string{"Film A", "Film C"}}, PersonSignals{NotableWorks: []string{"film c"}}, false},
		{
			"yoonseongho director vs singer real case",
			PersonSignals{PrimaryRole: "director", NotableWorks: []string{"우리는 매일매일"}},
			PersonSignals{PrimaryRole: "singer", Agency: "Indie", NotableWorks: []string{"바람이 분다"}},
			true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Conflict(c.a, c.b); got != c.want {
				t.Fatalf("Conflict()=%v want %v", got, c.want)
			}
		})
	}
}

func TestSuggestDisambig(t *testing.T) {
	cases := []struct {
		s    PersonSignals
		want string
	}{
		{PersonSignals{Agency: "YG"}, "(YG)"},
		{PersonSignals{PrimaryRole: "director"}, "(director)"},
		{PersonSignals{BirthYear: 1990}, "(1990)"},
		{PersonSignals{}, ""},
		{PersonSignals{PrimaryRole: "other"}, ""},
	}
	for _, c := range cases {
		if got := SuggestDisambig(c.s); got != c.want {
			t.Fatalf("SuggestDisambig(%+v)=%q want %q", c.s, got, c.want)
		}
	}
}
