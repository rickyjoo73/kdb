package kdb

import (
	"os"
	"strings"
)

func readSourceFile(name string) (string, error) {
	b, err := os.ReadFile(name)
	return string(b), err
}

func contains(hay, needle string) bool { return strings.Contains(hay, needle) }
