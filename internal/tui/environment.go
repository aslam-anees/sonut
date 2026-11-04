package tui

import "os"

func atcodeEnv(suffix string) string {
	if value, exists := os.LookupEnv("ATCODE_" + suffix); exists {
		return value
	}
	return os.Getenv("SONUT_" + suffix)
}
