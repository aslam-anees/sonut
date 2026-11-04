package sec

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type ownershipRule struct {
	pattern string
	owners  string
}

type ownershipResolver struct {
	rules []ownershipRule
}

func loadOwnership(root string) ownershipResolver {
	for _, relative := range []string{".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS"} {
		file, err := os.Open(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			continue
		}
		resolver := ownershipResolver{}
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 2 || strings.HasPrefix(fields[0], "!") {
				continue
			}
			pattern := strings.TrimSpace(fields[0])
			if strings.ContainsAny(pattern, "[]") {
				// Character classes are easy to misinterpret across CODEOWNERS
				// implementations; fail safely and leave the finding unowned.
				continue
			}
			resolver.rules = append(resolver.rules, ownershipRule{pattern: pattern, owners: strings.Join(fields[1:], " ")})
		}
		_ = file.Close()
		return resolver
	}
	return ownershipResolver{}
}

func (resolver ownershipResolver) Resolve(path string) string {
	path = strings.TrimPrefix(filepath.ToSlash(filepath.Clean(path)), "./")
	owner := ""
	for _, rule := range resolver.rules {
		if codeownersMatch(rule.pattern, path) {
			owner = rule.owners
		}
	}
	if owner == "" {
		return "unowned"
	}
	return owner
}

func codeownersMatch(pattern, path string) bool {
	pattern = strings.TrimSpace(filepath.ToSlash(pattern))
	pattern = strings.TrimPrefix(pattern, "./")
	directory := strings.HasSuffix(pattern, "/")
	pattern = strings.TrimPrefix(pattern, "/")
	pattern = strings.TrimSuffix(pattern, "/")
	if pattern == "" {
		return false
	}
	if strings.HasPrefix(pattern, "**/") {
		pattern = strings.TrimPrefix(pattern, "**/")
	}
	if directory {
		return strings.HasPrefix(path, strings.TrimSuffix(pattern, "/")+"/")
	}
	if !strings.Contains(pattern, "/") {
		for _, part := range strings.Split(path, "/") {
			if wildcardMatch(pattern, part) {
				return true
			}
		}
		return false
	}
	return wildcardMatch(pattern, path)
}

func wildcardMatch(pattern, value string) bool {
	var b strings.Builder
	b.WriteString("^")
	for index := 0; index < len(pattern); index++ {
		switch pattern[index] {
		case '*':
			if index+1 < len(pattern) && pattern[index+1] == '*' {
				b.WriteString(".*")
				index++
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(pattern[index])))
		}
	}
	b.WriteString("$")
	matched, err := regexp.MatchString(b.String(), value)
	return err == nil && matched
}

func applyOwnership(report *Report) {
	resolver := loadOwnership(report.Root)
	for index := range report.Findings {
		finding := &report.Findings[index]
		finding.RepositoryRoot = report.Root
		finding.Owner = resolver.Resolve(finding.Path)
		if finding.Path != "" {
			finding.FixTarget = finding.Path
			if finding.Line > 0 {
				finding.FixTarget += ":" + itoa(finding.Line)
			}
		}
		report.FixPlans = append(report.FixPlans, FixPlan{
			FindingOrPathID: finding.ID,
			TargetPath:      finding.Path,
			TargetLine:      finding.Line,
			RepositoryRoot:  report.Root,
			Service:         finding.Service,
			Owner:           finding.Owner,
			Summary:         finding.Remediation,
			Verification:    finding.Verification,
			GenerationMode:  GenerationDeterministic,
		})
	}
	for index := range report.RiskPaths {
		path := &report.RiskPaths[index]
		for _, finding := range report.Findings {
			if path.ID == "path:"+finding.ID {
				path.Owner = finding.Owner
				path.Service = finding.Service
				path.FixTarget = finding.FixTarget
				break
			}
		}
		if path.Owner == "" {
			path.Owner = "unowned"
		}
	}
}

func itoa(value int) string {
	if value <= 0 {
		return "0"
	}
	var digits [20]byte
	index := len(digits)
	for value > 0 {
		index--
		digits[index] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[index:])
}
