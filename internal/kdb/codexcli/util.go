package codexcli

import "sort"

// orderedKeys returns map keys in a deterministic (sorted) order.
//
// NOTE: server.mjs relied on JS Object.entries() insertion order for the
// spellings/known/sitelinks blocks. Go map iteration is random, so we sort
// keys to keep prompt output stable and reproducible. This only affects the
// ORDER of locale lines inside a block, not their content.
func orderedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
