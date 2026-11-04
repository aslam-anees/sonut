package tools

import "testing"

func TestStripToolProtocolResidue(t *testing.T) {
	got, stripped := stripToolProtocolResidue("<html>ok</html>\n</function>\n<tool_call>next")
	if !stripped || got != "<html>ok</html>\n" {
		t.Fatalf("got %q, stripped=%v", got, stripped)
	}
}

func TestStripToolProtocolResidueLeavesNormalContent(t *testing.T) {
	input := "const marker = '<tool_call>';\n"
	got, stripped := stripToolProtocolResidue(input)
	if stripped || got != input {
		t.Fatalf("normal content changed: got %q, stripped=%v", got, stripped)
	}
}
