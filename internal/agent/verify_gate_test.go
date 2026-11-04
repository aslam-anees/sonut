package agent

import "testing"

func TestClaimsCompletion(t *testing.T) {
	positives := []string{
		"Done! The parser is fixed.",
		"I've updated main.go with the new flag.",
		"The feature is now implemented and ready to use.",
		"All set — the config file now works.",
	}
	for _, text := range positives {
		if !claimsCompletion(text) {
			t.Errorf("claimsCompletion(%q) = false, want true", text)
		}
	}
	negatives := []string{
		"",
		"Which of the two approaches do you prefer?",
		"I found three candidate causes; investigating the first.",
	}
	for _, text := range negatives {
		if claimsCompletion(text) {
			t.Errorf("claimsCompletion(%q) = true, want false", text)
		}
	}
}
