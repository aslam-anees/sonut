package tools

import (
	"testing"
)

func TestFuzzyReplace(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		oldString  string
		newString  string
		replaceAll bool
		wantOut    string
		wantFound  bool
	}{
		{
			name:      "exact leading indentation mismatch",
			content:   "\t\tfoo bar baz\n",
			oldString: "foo bar baz",
			newString: "qux",
			wantOut:   "\t\tqux\n",
			wantFound: true,
		},
		{
			name:      "collapsed whitespace in middle",
			content:   "hello   world\n",
			oldString: "hello world",
			newString: "hi earth",
			wantOut:   "hi earth\n",
			wantFound: true,
		},
		{
			name:      "emoji in content survives untouched",
			content:   "func foo() {\n\t// 🎉 celebrate\n}\n",
			oldString: "// 🎉 celebrate",
			newString: "// done",
			wantOut:   "func foo() {\n\t// done\n}\n",
			wantFound: true,
		},
		{
			name:      "leading whitespace file",
			content:   "   if x {\n   }\n",
			oldString: "if x {\n   }",
			newString: "if y {}",
			wantOut:   "   if y {}\n",
			wantFound: true,
		},
		{
			name:       "replace all finds multiple",
			content:    "foo  bar\nfoo  bar\n",
			oldString:  "foo bar",
			newString:  "baz",
			replaceAll: true,
			wantOut:    "baz\nbaz\n",
			wantFound:  true,
		},
		{
			name:      "no match returns false",
			content:   "hello world\n",
			oldString: "goodbye world",
			wantFound: false,
		},
		{
			name:      "multi-byte UTF-8 offset stays correct",
			content:   "// élève\nfoo\n",
			oldString: "foo",
			newString: "bar",
			wantOut:   "// élève\nbar\n",
			wantFound: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, found := fuzzyReplace(tc.content, tc.oldString, tc.newString, tc.replaceAll)
			if found != tc.wantFound {
				t.Fatalf("found=%v want %v", found, tc.wantFound)
			}
			if found && got != tc.wantOut {
				t.Errorf("output mismatch\ngot:  %q\nwant: %q", got, tc.wantOut)
			}
		})
	}
}

func TestFuzzyCount(t *testing.T) {
	content := "  foo  bar\n  foo  bar\n"
	if n := fuzzyCount(content, "foo bar"); n != 2 {
		t.Errorf("fuzzyCount = %d, want 2", n)
	}
}
