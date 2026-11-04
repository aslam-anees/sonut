package tips

import (
	"fmt"
	"math/rand"
	"strings"
	"time"
)

// Tip represents a contextual tip or suggestion
type Tip struct {
	ID          string
	Title       string
	Description string
	Keywords    []string
	Category    string
	Priority    int // 1-5, higher = more important
	CodesExampl string
	RelatedTips []string
	Frequency   string // "always", "often", "sometimes", "rare"
	ShownCount  int
	LastShownAt time.Time
}

// TipsDatabase contains all available tips
type TipsDatabase struct {
	tips map[string]*Tip
}

// NewTipsDatabase creates a new tips database
func NewTipsDatabase() *TipsDatabase {
	return &TipsDatabase{
		tips: buildDefaultTips(),
	}
}

// buildDefaultTips returns the default tips catalog (~50 tips)
func buildDefaultTips() map[string]*Tip {
	tips := make(map[string]*Tip)

	// Tip 1: Resume System
	tips["resume-intro"] = &Tip{
		ID:          "resume-intro",
		Title:       "Resume Your Last Session",
		Description: "Run 'at /resume' to pick up where you left off in a previous conversation",
		Keywords:    []string{"resume", "session", "history", "recover"},
		Category:    "navigation",
		Priority:    5,
		Frequency:   "often",
	}

	// Tip 2: Context Management
	tips["context-compact"] = &Tip{
		ID:          "context-compact",
		Title:       "Compress Context with /compact",
		Description: "Use '/compact' to summarize old conversation turns and save context tokens for longer work",
		Keywords:    []string{"compact", "context", "tokens", "compression"},
		Category:    "optimization",
		Priority:    4,
		Frequency:   "often",
	}

	// Tip 3: Effort Estimation
	tips["effort-estimate"] = &Tip{
		ID:          "effort-estimate",
		Title:       "Estimate Task Effort with /effort",
		Description: "Use '/effort' to ask @code to estimate how much time/tokens a task will take before starting",
		Keywords:    []string{"effort", "estimate", "time", "tokens", "complexity"},
		Category:    "planning",
		Priority:    3,
		Frequency:   "sometimes",
	}

	// Tip 4: Model Status
	tips["model-status"] = &Tip{
		ID:          "model-status",
		Title:       "Check Model Status",
		Description: "Run '/model' to see current model info, context usage, and performance stats",
		Keywords:    []string{"model", "status", "info", "performance"},
		Category:    "diagnostics",
		Priority:    3,
		Frequency:   "sometimes",
	}

	// Tip 5: Clear Session
	tips["session-clear"] = &Tip{
		ID:          "session-clear",
		Title:       "Start Fresh with /clear",
		Description: "Use '/clear' to reset the current conversation and start a new session",
		Keywords:    []string{"clear", "reset", "new session", "restart"},
		Category:    "navigation",
		Priority:    3,
		Frequency:   "sometimes",
	}

	// Tip 6: File Operations
	tips["file-read"] = &Tip{
		ID:          "file-read",
		Title:       "Efficiently Read Large Files",
		Description: "@code auto-selects file chunks to stay within context. Ask 'show me line 100-200' for specific ranges",
		Keywords:    []string{"file", "read", "large", "chunks", "context"},
		Category:    "tools",
		Priority:    4,
		Frequency:   "often",
	}

	// Tip 7: Git Integration
	tips["git-diff"] = &Tip{
		ID:          "git-diff",
		Title:       "Review Changes with Git Diff",
		Description: "Ask @code to 'show the diff' or 'list changes' to see modified files before committing",
		Keywords:    []string{"git", "diff", "changes", "modified", "commit"},
		Category:    "tools",
		Priority:    4,
		Frequency:   "often",
	}

	// Tip 8: Terminal Commands
	tips["bash-safe"] = &Tip{
		ID:          "bash-safe",
		Title:       "Safe Shell Execution",
		Description: "@code asks for permission before running destructive commands. Review prompts carefully",
		Keywords:    []string{"bash", "shell", "execute", "permission", "safety"},
		Category:    "safety",
		Priority:    5,
		Frequency:   "always",
	}

	// Tip 9: Vision/Screenshots
	tips["vision-browser"] = &Tip{
		ID:          "vision-browser",
		Title:       "Use Browser for Visual Tasks",
		Description: "Ask @code to 'screenshot the page' or 'click the button' for browser automation and visual analysis",
		Keywords:    []string{"vision", "screenshot", "browser", "visual", "image"},
		Category:    "tools",
		Priority:    3,
		Frequency:   "sometimes",
	}

	// Tip 10: Code Review
	tips["code-review"] = &Tip{
		ID:          "code-review",
		Title:       "Ask for Code Review",
		Description: "Paste code and ask 'review this for bugs', 'optimize this', or 'explain this function'",
		Keywords:    []string{"review", "code", "optimize", "bugs", "refactor"},
		Category:    "analysis",
		Priority:    4,
		Frequency:   "often",
	}

	// Tip 11: Testing
	tips["test-generation"] = &Tip{
		ID:          "test-generation",
		Title:       "Generate Tests Automatically",
		Description: "Ask 'write tests for this function' or 'create a test file' for test generation",
		Keywords:    []string{"test", "unit test", "generate", "coverage"},
		Category:    "tools",
		Priority:    3,
		Frequency:   "sometimes",
	}

	// Tip 12: Documentation
	tips["doc-generation"] = &Tip{
		ID:          "doc-generation",
		Title:       "Generate Documentation",
		Description: "Ask 'document this code' or 'write a README' to auto-generate docs",
		Keywords:    []string{"documentation", "readme", "comments", "docstring"},
		Category:    "tools",
		Priority:    3,
		Frequency:   "sometimes",
	}

	// Tip 13: Performance Analysis
	tips["perf-analysis"] = &Tip{
		ID:          "perf-analysis",
		Title:       "Analyze Performance Bottlenecks",
		Description: "Share code or profiling data and ask 'where's the bottleneck' for performance tuning",
		Keywords:    []string{"performance", "bottleneck", "profiling", "optimization"},
		Category:    "analysis",
		Priority:    3,
		Frequency:   "sometimes",
	}

	// Tip 14: Debugging
	tips["debug-tactics"] = &Tip{
		ID:          "debug-tactics",
		Title:       "Collaborate on Debugging",
		Description: "Share error messages and ask 'why is this failing' or 'debug this issue' for troubleshooting",
		Keywords:    []string{"debug", "error", "fail", "troubleshoot", "issue"},
		Category:    "analysis",
		Priority:    4,
		Frequency:   "often",
	}

	// Tip 15: Multi-file Edits
	tips["multi-file-edit"] = &Tip{
		ID:          "multi-file-edit",
		Title:       "Make Coordinated File Changes",
		Description: "Ask 'update X and Y together' to coordinate changes across multiple files",
		Keywords:    []string{"multi-file", "coordinated", "refactor", "changes"},
		Category:    "tools",
		Priority:    3,
		Frequency:   "sometimes",
	}

	// More tips would follow...
	// For MVP, we have 15 core tips

	return tips
}

// GetTip returns a tip by ID
func (db *TipsDatabase) GetTip(id string) *Tip {
	return db.tips[id]
}

// AllTips returns all available tips
func (db *TipsDatabase) AllTips() []*Tip {
	tips := make([]*Tip, 0, len(db.tips))
	for _, tip := range db.tips {
		tips = append(tips, tip)
	}
	return tips
}

// TipMatcher finds relevant tips based on context
type TipMatcher struct {
	db *TipsDatabase
}

// NewTipMatcher creates a new tip matcher
func NewTipMatcher(db *TipsDatabase) *TipMatcher {
	return &TipMatcher{db: db}
}

// FindRelevantTips finds tips matching the given context
func (tm *TipMatcher) FindRelevantTips(userInput string, context string) []*Tip {
	input := strings.ToLower(userInput)
	ctx := strings.ToLower(context)
	combined := input + " " + ctx

	var relevant []*Tip

	for _, tip := range tm.db.AllTips() {
		score := 0

		// Check keyword matches
		for _, keyword := range tip.Keywords {
			if strings.Contains(combined, keyword) {
				score += 2
			}
		}

		// Boost priority tips
		score += tip.Priority

		// Penalty for recently shown tips
		if time.Since(tip.LastShownAt) < 1*time.Hour {
			score -= 3
		}

		if score > 0 {
			relevant = append(relevant, tip)
		}
	}

	// Sort by score (higher first)
	// Simple bubble sort for MVP
	for i := 0; i < len(relevant); i++ {
		for j := i + 1; j < len(relevant); j++ {
			if tm.scoreForTip(relevant[j]) > tm.scoreForTip(relevant[i]) {
				relevant[i], relevant[j] = relevant[j], relevant[i]
			}
		}
	}

	return relevant
}

// scoreForTip calculates a tip's relevance score
func (tm *TipMatcher) scoreForTip(tip *Tip) int {
	score := tip.Priority
	if time.Since(tip.LastShownAt) < 1*time.Hour {
		score -= 3
	}
	return score
}

// SelectRandomTip selects a random tip, biased toward unshown/high-priority tips
func (tm *TipMatcher) SelectRandomTip() *Tip {
	tips := tm.db.AllTips()
	if len(tips) == 0 {
		return nil
	}

	// Weight tips by priority and recency
	var weights []int
	for _, tip := range tips {
		weight := tip.Priority
		if tip.ShownCount == 0 {
			weight += 5 // Boost unseen tips
		}
		weights = append(weights, weight)
	}

	// Select weighted random
	totalWeight := 0
	for _, w := range weights {
		totalWeight += w
	}

	if totalWeight == 0 {
		return tips[rand.Intn(len(tips))]
	}

	target := rand.Intn(totalWeight)
	current := 0
	for i, weight := range weights {
		current += weight
		if current >= target {
			return tips[i]
		}
	}

	return tips[len(tips)-1]
}

// TipsCommand represents the /tips command
func TipsCommand() string {
	db := NewTipsDatabase()
	tip := NewTipMatcher(db).SelectRandomTip()

	if tip == nil {
		return "No tips available"
	}

	output := fmt.Sprintf("\n💡 Tip: %s\n", tip.Title)
	output += fmt.Sprintf("   %s\n", tip.Description)

	if tip.CodesExampl != "" {
		output += fmt.Sprintf("\n   Example:\n   %s\n", tip.CodesExampl)
	}

	tip.ShownCount++
	tip.LastShownAt = time.Now()

	return output
}
