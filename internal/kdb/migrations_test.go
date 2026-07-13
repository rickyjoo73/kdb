package kdb

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"
)

func TestMigrationNumbersAreUnique(t *testing.T) {
	entries, err := os.ReadDir("../../migrations")
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	re := regexp.MustCompile(`^(\d{4})_.*\.sql$`)
	byNumber := map[string][]string{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		match := re.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		byNumber[match[1]] = append(byNumber[match[1]], filepath.Join("migrations", entry.Name()))
	}
	var duplicates []string
	for number, files := range byNumber {
		if len(files) > 1 {
			sort.Strings(files)
			duplicates = append(duplicates, number+": "+files[0]+", "+files[1])
		}
	}
	if len(duplicates) > 0 {
		sort.Strings(duplicates)
		t.Fatalf("duplicate migration numbers: %v", duplicates)
	}
}
