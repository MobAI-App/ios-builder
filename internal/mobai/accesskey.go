package mobai

import (
	"bufio"
	"os"
	"strings"
)

// MobAI with an API token set admits callers from other hosts, which includes
// builder in WSL reaching MobAI on Windows, only when they send the token.
// Without it every endpoint but health fails with 401 UNAUTHORIZED. Callers on
// the MobAI host itself never need it.
const (
	accessKeyEnv    = "MOBAI_ACCESS_KEY"
	accessKeyHeader = "X-API-Key"
)

// accessKey returns MOBAI_ACCESS_KEY from the environment, or else from .env in
// the working directory. Flutter runs the custom device commands from the
// project directory, so they read the same .env as `builder dev flutter`.
func accessKey() string {
	if key := strings.TrimSpace(os.Getenv(accessKeyEnv)); key != "" {
		return key
	}
	return dotEnvValue(".env", accessKeyEnv)
}

// dotEnvValue returns key from a .env file of KEY=value lines, allowing an
// "export " prefix, matching quotes and trailing # comments. It returns "" when
// the file or key is missing.
func dotEnvValue(path, key string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		name, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(name) != key {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			return value[1 : len(value)-1]
		}
		if i := strings.Index(value, " #"); i >= 0 {
			value = strings.TrimSpace(value[:i])
		}
		return value
	}
	return ""
}
