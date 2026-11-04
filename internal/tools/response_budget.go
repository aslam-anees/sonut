package tools

// Fair batch tool-response budgeting (qwen-code tool-response-finalizer port).
// When several tool bodies land in one model turn, a fixed per-tool cap still
// lets the sum blow local prefill. allocateTextBudget divides a shared budget
// fairly so small results stay intact and large ones shrink together.

import "strings"

// AllocateTextBudget fairly assigns maxChars across lengths so the sum of
// allocations never exceeds budget. Short texts keep their full length; the
// remainder is shared among the long ones.
func AllocateTextBudget(lengths []int, budget int) []int {
	n := len(lengths)
	allocations := make([]int, n)
	if n == 0 {
		return allocations
	}
	remaining := budget
	if remaining < 0 {
		remaining = 0
	}
	active := make([]int, 0, n)
	for i := range lengths {
		if lengths[i] < 0 {
			lengths[i] = 0
		}
		active = append(active, i)
	}
	for len(active) > 0 {
		share := remaining / len(active)
		fixed := make([]int, 0, len(active))
		for _, index := range active {
			if lengths[index] <= share {
				fixed = append(fixed, index)
			}
		}
		if len(fixed) == 0 {
			for _, index := range active {
				allocations[index] = share
			}
			remainder := remaining - share*len(active)
			for _, index := range active {
				if remainder == 0 {
					break
				}
				allocations[index]++
				remainder--
			}
			break
		}
		fixedSet := map[int]bool{}
		for _, index := range fixed {
			fixedSet[index] = true
			allocations[index] = lengths[index]
			remaining -= lengths[index]
		}
		next := active[:0]
		for _, index := range active {
			if !fixedSet[index] {
				next = append(next, index)
			}
		}
		active = next
	}
	return allocations
}

// FitTextHeadTail keeps the start and end of oversized text so stack traces and
// command summaries remain useful after truncation.
func FitTextHeadTail(text string, maxBytes int, note string) string {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text
	}
	if note == "" {
		note = "\n…[truncated]…\n"
	}
	if len(note) >= maxBytes {
		return utf8Prefix(text, maxBytes)
	}
	keep := maxBytes - len(note)
	head := keep * 2 / 3
	tail := keep - head
	if head < 32 {
		head = keep / 2
		tail = keep - head
	}
	return utf8Prefix(text, head) + note + utf8Suffix(text, tail)
}

// ApplyBatchOutputBudget mutates tool body strings in place under a shared
// character budget. Empty bodies and already-small sets are no-ops. Returns
// how many bodies were shortened.
func ApplyBatchOutputBudget(bodies []string, budgetBytes int, hint string) int {
	if budgetBytes <= 0 || len(bodies) == 0 {
		return 0
	}
	lengths := make([]int, len(bodies))
	total := 0
	for i, body := range bodies {
		lengths[i] = len(body)
		total += lengths[i]
	}
	if total <= budgetBytes {
		return 0
	}
	allocations := AllocateTextBudget(lengths, budgetBytes)
	if hint == "" {
		hint = "re-read with a tighter range if you need more"
	}
	note := "\n\n[truncated: shared tool-response budget; " + hint + "]\n"
	changed := 0
	for i, body := range bodies {
		if lengths[i] <= allocations[i] {
			continue
		}
		// Leave room for the note inside the allocation when possible.
		capBytes := allocations[i]
		if capBytes > len(note)+64 {
			capBytes -= len(note)
			bodies[i] = FitTextHeadTail(body, capBytes, note)
		} else {
			bodies[i] = FitTextHeadTail(body, allocations[i], "\n…\n")
		}
		changed++
	}
	return changed
}

// JoinBudgetedFiles concatenates labeled file sections under a total byte budget.
func JoinBudgetedFiles(sections []string, budgetBytes int) string {
	if len(sections) == 0 {
		return ""
	}
	if budgetBytes <= 0 {
		return strings.Join(sections, "\n\n")
	}
	bodies := make([]string, len(sections))
	copy(bodies, sections)
	ApplyBatchOutputBudget(bodies, budgetBytes, "use read_file on a single path for full content")
	return strings.Join(bodies, "\n\n")
}
