package kentity

// Compatibility categories are not identity proof. Preserve the original
// subtype and whether TDB treats that record as geographic.
type TDBType struct {
	EntityType, Label string
	Geographic        bool
}

var tdbTypes = map[string]TDBType{
	"tourist_spot": {"location", "관광지", true}, "legal_dong": {"location", "법정동", true},
	"cultural_facility": {"location", "문화시설", true}, "festival_event": {"event", "행사·공연·축제", true},
	"travel_course": {"location", "여행코스", true}, "leisure_sports": {"location", "레포츠 시설", true},
	"accommodation": {"location", "숙박 시설", true}, "shopping": {"location", "쇼핑 시설", true},
	"restaurant": {"location", "음식점", true}, "transport": {"location", "교통", true},
	"heritage": {"location", "문화유산", true}, "nature": {"location", "자연지명", true},
	"admin_region": {"location", "행정구역", true}, "road": {"location", "도로", true},
	"district": {"location", "상권·지구", true}, "other": {"unknown", "기타·추가 구분 필요", true},
	"food": {"concept", "음식·메뉴", false}, "organization": {"organization", "기관·기업", false},
	"transit": {"location", "교통시설·노선", true}, "person": {"person", "인물", false},
	"work": {"work", "작품·유물", false}, "education": {"unknown", "학교 법인·캠퍼스 구분 필요", true},
}

func TDBTypeFor(code string) (TDBType, bool) { v, ok := tdbTypes[code]; return v, ok }

func tdbTypeCompatible(source, target string) bool {
	typ, ok := TDBTypeFor(source)
	if !ok || target == "unknown" || !supportedTypes[target] {
		return false
	}
	if source == "education" {
		return target == "organization" || target == "location"
	}
	if typ.EntityType == "unknown" {
		return true
	}
	if source == "organization" {
		return target == "organization" || target == "company" || target == "team" || target == "league"
	}
	return typ.EntityType == target
}

// A shop located by a station is not the station itself. This negative gate
// uses source classes, never substrings such as "역" in a business name.
// Absence of a known conflicting class is not identity approval.
func TDBSourceClassConflict(source string, instanceOf []string) bool {
	typ, known := TDBTypeFor(source)
	if !known {
		return false
	}
	person := containsString(instanceOf, "Q5")
	if typ.EntityType != "unknown" && (typ.EntityType == "person") != person {
		return true
	}
	if source == "education" && person {
		return true
	}
	if source == "restaurant" || source == "shopping" || source == "accommodation" {
		return containsString(instanceOf, "Q928830") || containsString(instanceOf, "Q22808403")
	}
	return false
}
