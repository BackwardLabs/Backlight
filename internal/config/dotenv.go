package config

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"unicode"
)

func loadDotEnvFiles(paths ...string) {
	locked := currentEnvKeys()
	for _, path := range paths {
		loadDotEnvFile(path, locked)
	}
}

func currentEnvKeys() map[string]struct{} {
	out := make(map[string]struct{})
	for _, entry := range os.Environ() {
		if key, _, ok := strings.Cut(entry, "="); ok {
			out[key] = struct{}{}
		}
	}
	return out
}

func loadDotEnvFile(path string, locked map[string]struct{}) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		key, value, ok := parseDotEnvLine(scanner.Text())
		if !ok {
			continue
		}
		if _, exists := locked[key]; exists {
			continue
		}
		_ = os.Setenv(key, value)
	}
}

func parseDotEnvLine(line string) (string, string, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
	key, value, ok := strings.Cut(line, "=")
	if !ok {
		return "", "", false
	}
	key = strings.TrimSpace(key)
	if !validEnvKey(key) {
		return "", "", false
	}
	return key, parseDotEnvValue(strings.TrimSpace(value)), true
}

func parseDotEnvValue(value string) string {
	if len(value) >= 2 {
		quote := value[0]
		if quote == '"' && value[len(value)-1] == '"' {
			if unquoted, err := strconv.Unquote(value); err == nil {
				return unquoted
			}
			return strings.Trim(value, `"`)
		}
		if quote == '\'' && value[len(value)-1] == '\'' {
			return value[1 : len(value)-1]
		}
	}
	return stripInlineComment(value)
}

func stripInlineComment(value string) string {
	inSpace := false
	for i, r := range value {
		if r == '#' && inSpace {
			return strings.TrimSpace(value[:i])
		}
		inSpace = unicode.IsSpace(r)
	}
	return strings.TrimSpace(value)
}

func validEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		if i == 0 {
			if r != '_' && !unicode.IsLetter(r) {
				return false
			}
			continue
		}
		if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}
