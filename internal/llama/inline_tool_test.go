package llama

import (
	"encoding/json"
	"testing"
)

func TestExtractInlineToolCodeCall(t *testing.T) {
	calls := extractInlineToolCalls(`<tool_code>
mcp_firecrawl_search(query="official Go website", limit=3, exact=True)
</tool_code>`)
	if len(calls) != 1 {
		t.Fatalf("calls = %#v, want one recovered call", calls)
	}
	if calls[0].name != "mcp_firecrawl_search" {
		t.Fatalf("name = %q", calls[0].name)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(calls[0].args), &args); err != nil {
		t.Fatalf("arguments are not JSON: %v", err)
	}
	if args["query"] != "official Go website" || args["limit"] != float64(3) || args["exact"] != true {
		t.Fatalf("arguments = %#v", args)
	}
}

func TestExtractInlineToolCodeRejectsInvalidArguments(t *testing.T) {
	if calls := extractInlineToolCalls(`<tool_code>write_file(path="x", content=not_valid)</tool_code>`); len(calls) != 0 {
		t.Fatalf("unsafe guessed call = %#v", calls)
	}
}

func TestExtractInlineInvokeCall(t *testing.T) {
	calls := extractInlineToolCalls(`<invoke name="firecrawl_search" arguments='{"query": "official Go website"}' />`)
	if len(calls) != 1 {
		t.Fatalf("calls = %#v, want one recovered call", calls)
	}
	if calls[0].name != "firecrawl_search" || calls[0].args != `{"query":"official Go website"}` {
		t.Fatalf("call = %#v", calls[0])
	}
}

func TestExtractFencedJSONToolCall(t *testing.T) {
	calls := extractInlineToolCalls("```json\n" +
		`{"name":"mcp_firecrawl_search","arguments":{"query":"official Go website"}}` +
		"\n```")
	if len(calls) != 1 {
		t.Fatalf("calls = %#v, want one recovered call", calls)
	}
	if calls[0].name != "mcp_firecrawl_search" || calls[0].args != `{"query":"official Go website"}` {
		t.Fatalf("call = %#v", calls[0])
	}
}

func TestExtractFunctionCallTags(t *testing.T) {
	raw := `<function_call>
<function_name>mcp_firecrawl_search</function_name>
<function_parameters>{"query":"official Go website"}</function_parameters>
</function_call>`
	calls := extractInlineToolCalls(raw)
	if len(calls) != 1 {
		t.Fatalf("calls = %#v, want one recovered call", calls)
	}
	if calls[0].name != "mcp_firecrawl_search" || calls[0].args != `{"query":"official Go website"}` {
		t.Fatalf("call = %#v", calls[0])
	}
}

func TestInlineToolStripperHandlesSplitTags(t *testing.T) {
	stripper := &inlineToolStripper{}
	got := stripper.feed("before <tool_")
	got += stripper.feed("code>read_file(path=\"x\")</tool_")
	got += stripper.feed("code> after")
	got += stripper.feed(" <inv")
	got += stripper.feed(`oke name="search" arguments='{}' /> done`)
	got += stripper.flush()
	if got != "before  after  done" {
		t.Fatalf("visible text = %q", got)
	}
}
