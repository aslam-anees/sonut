package processinsight

import (
	"strings"

	"github.com/aistatine/sonut/internal/redaction"
)

func redactArguments(value string) string {
	fields := strings.Fields(value)
	redactNext := false
	for index, field := range fields {
		if redactNext {
			fields[index] = redaction.RedactedSecret
			redactNext = false
			continue
		}
		key := strings.TrimLeft(field, "-")
		if name, _, ok := strings.Cut(key, "="); ok {
			if sensitiveArgumentKey(name) {
				fields[index] = strings.SplitN(field, "=", 2)[0] + "=" + redaction.RedactedSecret
				continue
			}
		} else if sensitiveArgumentKey(key) {
			redactNext = true
		}
		fields[index] = redaction.RedactString(field, redaction.Options{})
	}
	return strings.TrimSpace(strings.Join(fields, " "))
}

func sensitiveArgumentKey(key string) bool {
	key = strings.ToLower(strings.TrimLeft(key, "-"))
	if key == "" || strings.HasSuffix(key, "tokens") {
		return false
	}
	for _, marker := range []string{"password", "passwd", "secret", "credential", "token", "api-key", "apikey", "authorization", "auth"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return false
}
