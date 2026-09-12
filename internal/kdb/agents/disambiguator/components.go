package disambiguator

import (
	"github.com/google/uuid"
	"sort"
	"strings"
)

func connectedClusters(members []member, edges [][2]uuid.UUID) []cluster {
	ms := append([]member(nil), members...)
	sort.Slice(ms, func(i, j int) bool { return ms[i].id.String() < ms[j].id.String() })
	parent := make([]int, len(ms))
	index := map[uuid.UUID]int{}
	for i, m := range ms {
		parent[i] = i
		index[m.id] = i
	}
	var root func(int) int
	root = func(i int) int {
		if parent[i] != i {
			parent[i] = root(parent[i])
		}
		return parent[i]
	}
	join := func(a, b int) {
		a, b = root(a), root(b)
		if a < b {
			parent[b] = a
		} else {
			parent[a] = b
		}
	}
	for i := range ms {
		for j := 0; j < i; j++ {
			a, b := ms[i], ms[j]
			exact := normKey(a.ko) != "" && normKey(a.ko) == normKey(b.ko)
			sameType := a.entityType == b.entityType
			sameEN := strings.TrimSpace(a.en) != "" && strings.EqualFold(strings.TrimSpace(a.en), strings.TrimSpace(b.en))
			sameQID := validMergeQID.MatchString(a.qid) && a.qid == b.qid
			if exact || (sameType && (sameEN || sameQID)) {
				join(i, j)
			}
		}
	}
	for _, e := range edges {
		a, okA := index[e[0]]
		b, okB := index[e[1]]
		if okA && okB {
			join(a, b)
		}
	}
	groups := map[int][]member{}
	for i, m := range ms {
		r := root(i)
		groups[r] = append(groups[r], m)
	}
	var out []cluster
	for i := range ms {
		if g := groups[i]; len(g) > 1 {
			out = append(out, cluster{name: norm(g[0].ko), members: withWellFormed(g)})
		}
	}
	return out
}
