package sec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	threatIntelEndpoint       = "https://api.osv.dev/v1/query"
	threatIntelTimeout        = 5 * time.Second
	threatIntelResponseLimit  = 1 << 20
	threatIntelConcurrency    = 4
	threatIntelCacheDirectory = ".atcode/sec/cache"
)

type threatIntelConfig struct {
	ThreatIntel struct {
		Enabled bool `json:"enabled"`
	} `json:"threat_intel"`
	ThreatIntelEnabled bool `json:"threat_intel_enabled"`
}

type dependencyCoordinate struct {
	Package   string
	Ecosystem string
	Version   string
	Path      string
}

type threatIntelProvider interface {
	Lookup(context.Context, dependencyCoordinate) ([]ThreatIntelReference, error)
}

type osvProvider struct {
	client   *http.Client
	endpoint string
	cacheDir string
}

func securityConfigEnabled(root string) bool {
	data, err := os.ReadFile(filepath.Join(root, ".atcode", "security.json"))
	if err != nil {
		return false
	}
	var config threatIntelConfig
	if json.Unmarshal(data, &config) != nil {
		return false
	}
	return config.ThreatIntel.Enabled || config.ThreatIntelEnabled
}

func newOSVProvider(root string) threatIntelProvider {
	return &osvProvider{
		client:   &http.Client{Timeout: threatIntelTimeout},
		endpoint: threatIntelEndpoint,
		cacheDir: filepath.Join(root, filepath.FromSlash(threatIntelCacheDirectory)),
	}
}

func applyThreatIntel(report *Report, files map[string][]byte) {
	coordinates := dependencyCoordinates(files)
	if len(coordinates) == 0 {
		report.ThreatIntel = ThreatIntelSummary{Status: ThreatIntelFresh, Source: "OSV", Matches: 0}
		return
	}
	provider := newOSVProvider(report.Root)
	type result struct {
		coordinate dependencyCoordinate
		refs       []ThreatIntelReference
		cached     bool
		err        error
	}
	jobs := make(chan dependencyCoordinate)
	results := make(chan result, len(coordinates))
	workers := threatIntelConcurrency
	if workers > len(coordinates) {
		workers = len(coordinates)
	}
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for coordinate := range jobs {
				refs, err := provider.Lookup(context.Background(), coordinate)
				cached := false
				if osv, ok := provider.(*osvProvider); ok {
					cached = osv.lastLookupCached(coordinate)
				}
				results <- result{coordinate: coordinate, refs: refs, cached: cached, err: err}
			}
		}()
	}
	go func() {
		for _, coordinate := range coordinates {
			jobs <- coordinate
		}
		close(jobs)
		wait.Wait()
		close(results)
	}()

	cacheHits, successful, failures := 0, 0, 0
	var firstError error
	for item := range results {
		if item.err != nil {
			failures++
			if firstError == nil {
				firstError = item.err
			}
			continue
		}
		successful++
		if item.cached {
			cacheHits++
		}
		for _, reference := range item.refs {
			report.ThreatIntel.Matches++
			if reference.ExactMatch {
				report.ThreatIntel.ExactMatches++
			}
			appendThreatFinding(report, item.coordinate, reference)
		}
	}
	report.ThreatIntel.Source = "OSV"
	switch {
	case successful == 0 && failures > 0:
		report.ThreatIntel.Status = ThreatIntelUnavailable
		report.ThreatIntel.Error = boundedError(firstError)
	case failures > 0:
		report.ThreatIntel.Status = ThreatIntelError
		report.ThreatIntel.Error = boundedError(firstError)
	case cacheHits == successful:
		report.ThreatIntel.Status = ThreatIntelCached
	default:
		report.ThreatIntel.Status = ThreatIntelFresh
	}
}

func appendThreatFinding(report *Report, coordinate dependencyCoordinate, reference ThreatIntelReference) {
	path := coordinate.Path
	description := fmt.Sprintf("OSV reports exact version %s of %s as vulnerable. This is advisory evidence, not proof that the application is reachable or exploitable.", coordinate.Version, coordinate.Package)
	finding := Finding{
		RuleID: "threat-intel.osv", Title: "Exact dependency version has an OSV advisory", Description: description,
		Category: "supply-chain", Severity: SeverityHigh, RiskScore: 74, Path: path,
		Evidence: reference.AdvisoryID + " · exact version match", Confidence: reference.Confidence,
		EvidenceKind: "threat-intel", Remediation: "Upgrade to a version outside the advisory range, regenerate the lockfile, and run the dependency test suite.",
		Verification: "Re-run /sec with Threat intel enabled and confirm the exact-version advisory is no longer returned.",
		ThreatIntel:  []ThreatIntelReference{reference}, Factors: []string{"exact resolved version advisory"},
	}
	addFinding(report, &fileSignals{path: path}, finding)
}

var goRequirementPattern = regexp.MustCompile(`(?m)^\s*([A-Za-z0-9][^\s]+)\s+(v[0-9][^\s]+)`)

func dependencyCoordinates(files map[string][]byte) []dependencyCoordinate {
	seen := map[string]bool{}
	var result []dependencyCoordinate
	if data := files["go.mod"]; len(data) > 0 {
		for _, match := range goRequirementPattern.FindAllStringSubmatch(string(data), 256) {
			if len(match) != 3 {
				continue
			}
			coordinate := dependencyCoordinate{Package: match[1], Ecosystem: "Go", Version: match[2], Path: "go.mod"}
			key := coordinate.Ecosystem + "\x00" + coordinate.Package + "\x00" + coordinate.Version
			if !seen[key] {
				seen[key] = true
				result = append(result, coordinate)
			}
		}
	}
	if data := files["package-lock.json"]; len(data) > 0 {
		var lock struct {
			Packages map[string]struct {
				Version string `json:"version"`
			} `json:"packages"`
		}
		if json.Unmarshal(data, &lock) == nil {
			for path, packageInfo := range lock.Packages {
				if packageInfo.Version == "" || !strings.Contains(path, "node_modules/") {
					continue
				}
				name := strings.TrimPrefix(path[strings.LastIndex(path, "node_modules/")+len("node_modules/"):], "")
				if strings.HasPrefix(name, "@") {
					if slash := strings.Index(name[1:], "/"); slash >= 0 {
						name = name[:slash+2]
					}
				} else if slash := strings.IndexByte(name, '/'); slash >= 0 {
					name = name[:slash]
				}
				coordinate := dependencyCoordinate{Package: name, Ecosystem: "npm", Version: packageInfo.Version, Path: "package-lock.json"}
				key := coordinate.Ecosystem + "\x00" + coordinate.Package + "\x00" + coordinate.Version
				if name != "" && !seen[key] {
					seen[key] = true
					result = append(result, coordinate)
				}
			}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Ecosystem != result[j].Ecosystem {
			return result[i].Ecosystem < result[j].Ecosystem
		}
		if result[i].Package != result[j].Package {
			return result[i].Package < result[j].Package
		}
		return result[i].Version < result[j].Version
	})
	if len(result) > 128 {
		result = result[:128]
	}
	return result
}

type osvQueryResponse struct {
	Vulnerabilities []struct {
		ID         string    `json:"id"`
		Aliases    []string  `json:"aliases"`
		Published  time.Time `json:"published"`
		Modified   time.Time `json:"modified"`
		References []struct {
			Type string `json:"type"`
			URL  string `json:"url"`
		} `json:"references"`
		DatabaseSpecific map[string]any `json:"database_specific"`
	} `json:"vulns"`
}

func (provider *osvProvider) Lookup(ctx context.Context, coordinate dependencyCoordinate) ([]ThreatIntelReference, error) {
	if cached, ok := provider.readCache(coordinate); ok {
		provider.setLastLookupCached(coordinate, true)
		return cached, nil
	}
	provider.setLastLookupCached(coordinate, false)
	payload := map[string]any{"package": map[string]string{"name": coordinate.Package, "ecosystem": coordinate.Ecosystem}, "version": coordinate.Version}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, threatIntelTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, provider.endpoint, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := provider.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, threatIntelResponseLimit+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(data) > threatIntelResponseLimit {
		return nil, fmt.Errorf("OSV response exceeded %d bytes", threatIntelResponseLimit)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("OSV returned HTTP %d", response.StatusCode)
	}
	var decoded osvQueryResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	refs := make([]ThreatIntelReference, 0, len(decoded.Vulnerabilities))
	for _, vuln := range decoded.Vulnerabilities {
		url := ""
		for _, reference := range vuln.References {
			if strings.EqualFold(reference.Type, "WEB") || url == "" {
				url = reference.URL
			}
		}
		var published, modified *time.Time
		if !vuln.Published.IsZero() {
			value := vuln.Published
			published = &value
		}
		if !vuln.Modified.IsZero() {
			value := vuln.Modified
			modified = &value
		}
		refs = append(refs, ThreatIntelReference{Source: "OSV", AdvisoryID: vuln.ID, URL: url, Package: coordinate.Package, Ecosystem: coordinate.Ecosystem, ExactVersion: coordinate.Version, PublishedAt: published, ModifiedAt: modified, VulnerableVersionStatus: "vulnerable", KnownExploitation: explicitKnownExploitation(vuln.DatabaseSpecific), ExactMatch: true, Confidence: "high"})
	}
	if err := provider.writeCache(coordinate, refs); err != nil {
		// Cache failure must never turn a successful provider response into a
		// failed base scan or expose workspace details.
		return refs, nil
	}
	return refs, nil
}

func explicitKnownExploitation(values map[string]any) bool {
	for _, key := range []string{"known_exploitation", "knownExploitation", "known_exploit", "knownExploit"} {
		if value, ok := values[key].(bool); ok {
			return value
		}
	}
	return false
}

func (provider *osvProvider) cachePath(coordinate dependencyCoordinate) string {
	sum := sha256.Sum256([]byte(coordinate.Ecosystem + "\x00" + coordinate.Package + "\x00" + coordinate.Version))
	return filepath.Join(provider.cacheDir, hex.EncodeToString(sum[:])+".json")
}

func (provider *osvProvider) readCache(coordinate dependencyCoordinate) ([]ThreatIntelReference, bool) {
	path := provider.cachePath(coordinate)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	// Repair permissions on caches created by an older build or a copied
	// workspace before treating the entry as usable.
	_ = os.Chmod(path, 0o600)
	var refs []ThreatIntelReference
	if json.Unmarshal(data, &refs) != nil {
		return nil, false
	}
	return refs, true
}

func (provider *osvProvider) writeCache(coordinate dependencyCoordinate, refs []ThreatIntelReference) error {
	if err := os.MkdirAll(provider.cacheDir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(refs)
	if err != nil {
		return err
	}
	path := provider.cachePath(coordinate)
	tmp, err := os.CreateTemp(provider.cacheDir, ".osv-*")
	if err != nil {
		return err
	}
	tempPath := tmp.Name()
	defer os.Remove(tempPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

var lastLookup = struct {
	sync.Mutex
	values map[string]bool
}{values: map[string]bool{}}

func (provider *osvProvider) setLastLookupCached(coordinate dependencyCoordinate, value bool) {
	lastLookup.Lock()
	defer lastLookup.Unlock()
	lastLookup.values[provider.cachePath(coordinate)] = value
}

func (provider *osvProvider) lastLookupCached(coordinate dependencyCoordinate) bool {
	lastLookup.Lock()
	defer lastLookup.Unlock()
	return lastLookup.values[provider.cachePath(coordinate)]
}

func boundedError(err error) string {
	if err == nil {
		return ""
	}
	value := strings.TrimSpace(err.Error())
	if len(value) > 180 {
		value = value[:180] + "…"
	}
	return value
}
