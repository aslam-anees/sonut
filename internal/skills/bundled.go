package skills

// Bundled skills ship inside the binary via go:embed so core guidance (e.g.
// frontend-design) is available offline with zero installation. They merge
// into every Load with the LOWEST precedence: a disk skill with the same
// frontmatter name always shadows a bundled one, so users can override or
// customize bundled guidance simply by installing a skill of the same name.

import (
	"embed"
	"io/fs"
	"path"
	"sort"
)

//go:embed bundled/*/SKILL.md
var bundledFS embed.FS

// Bundled parses every embedded SKILL.md. Errors are impossible at
// runtime for a well-formed embed (contents are fixed at compile time), so
// malformed entries are simply skipped, mirroring the disk loader's tolerance.
func Bundled() []Skill {
	entries, err := fs.ReadDir(bundledFS, "bundled")
	if err != nil {
		return nil
	}
	loaded := make([]Skill, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		manifest := path.Join("bundled", entry.Name(), skillFileName)
		data, err := fs.ReadFile(bundledFS, manifest)
		if err != nil {
			continue
		}
		skill := parseSkill(entry.Name(), "builtin:"+entry.Name(), string(data))
		loaded = append(loaded, skill)
	}
	sort.Slice(loaded, func(left int, right int) bool {
		return loaded[left].Name < loaded[right].Name
	})
	return loaded
}

// MergeBundled appends bundled skills whose names are not already claimed by a
// disk skill, keeping the combined slice sorted by name.
func MergeBundled(diskSkills []Skill) []Skill {
	taken := make(map[string]bool, len(diskSkills))
	for _, skill := range diskSkills {
		taken[skill.Name] = true
	}
	merged := diskSkills
	for _, skill := range Bundled() {
		if !taken[skill.Name] {
			merged = append(merged, skill)
		}
	}
	sort.Slice(merged, func(left int, right int) bool {
		return merged[left].Name < merged[right].Name
	})
	return merged
}
