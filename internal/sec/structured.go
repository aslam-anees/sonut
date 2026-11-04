package sec

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	maxStructuredYAMLDocuments = 16
	maxStructuredDepth         = 24
	maxStructuredAssets        = 512
	maxStructuredEdges         = 1024
	maxStructuredRoutes        = 512
)

var (
	dockerfileExposePattern  = regexp.MustCompile(`(?im)^\s*EXPOSE\s+(.+)$`)
	dockerfileUserPattern    = regexp.MustCompile(`(?im)^\s*USER\s+([^\s#]+)`)
	dockerfileExecPattern    = regexp.MustCompile(`(?im)^\s*(ENTRYPOINT|CMD)\s+(.+)$`)
	terraformResourcePattern = regexp.MustCompile(`(?m)^\s*resource\s+"([^"]+)"\s+"([^"]+)"\s*\{`)
)

func collectStructured(report *Report, files map[string][]byte) {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		data := files[path]
		if isComposeCandidate(path) || isKubernetesCandidate(path) || workflowFile(path) || isRouteCandidate(path) {
			report.Coverage.StructuredFiles++
			if !parseStructuredYAML(report, path, data) {
				report.Coverage.StructuredMalformed++
			}
		}
		if strings.EqualFold(filepath.Base(path), "dockerfile") || strings.EqualFold(filepath.Base(path), "containerfile") {
			collectDockerfile(report, path, string(data))
		}
		if strings.EqualFold(filepath.Ext(path), ".tf") || strings.EqualFold(filepath.Ext(path), ".hcl") {
			collectTerraform(report, path, string(data))
		}
	}
	joinStructuredAssets(report)
	finalizeStructuredEdges(report)
}

func parseStructuredYAML(report *Report, path string, data []byte) bool {
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	parsed := false
	for document := 0; document < maxStructuredYAMLDocuments; document++ {
		var value any
		err := decoder.Decode(&value)
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "eof") {
				break
			}
			return parsed
		}
		if value == nil {
			continue
		}
		if structuredDepth(value, 0) > maxStructuredDepth {
			return parsed
		}
		parsed = true
		root, ok := asStringMap(value)
		if !ok {
			continue
		}
		kind := strings.ToLower(stringValue(root["kind"]))
		switch {
		case isComposeCandidate(path) && root["services"] != nil:
			collectCompose(report, path, root)
		case kind == "service" || kind == "ingress" || kind == "deployment" || kind == "statefulset" || kind == "daemonset" || kind == "pod":
			collectKubernetes(report, path, root, document)
		case workflowFile(path) && root["jobs"] != nil:
			collectWorkflow(report, path, root)
		}
		if root["routes"] != nil && isRouteCandidate(path) {
			collectRouteConfiguration(report, path, root)
		}
	}
	return parsed
}

func collectCompose(report *Report, path string, root map[string]any) {
	services, ok := asStringMap(root["services"])
	if !ok {
		return
	}
	for name, raw := range services {
		service, _ := asStringMap(raw)
		id := "compose:" + path + ":service:" + name
		labels := map[string]string{"identity": id}
		ports := collectPorts(service["ports"])
		if len(ports) > 0 {
			labels["published_ports"] = joinInts(ports)
		}
		if boolValue(service["privileged"]) {
			labels["privileged"] = "true"
		}
		if strings.EqualFold(stringValue(service["network_mode"]), "host") {
			labels["host_network"] = "true"
		}
		if containsString(stringValue(service["volumes"]), "/var/run/docker.sock") || containsValue(service["volumes"], "/var/run/docker.sock") {
			labels["docker_socket"] = "true"
		}
		if service["volumes"] != nil {
			labels["volumes_declared"] = "true"
		}
		if service["environment"] != nil || service["env_file"] != nil || service["secrets"] != nil {
			labels["secret_or_env_reference"] = "true"
		}
		addAsset(report, Asset{
			ID: id, Name: name, Kind: "service", Provider: "docker", Format: "compose",
			Service: name, Repository: report.Root, SourcePath: path, SourceLine: 1,
			Owner: "unowned", Labels: labels, EvidenceIDs: []string{structuredEvidence(path, 1, "compose-service")},
		})
		if len(ports) > 0 {
			state := ExposureConfiguredPublic
			entry := "published port"
			if binding := firstHostBinding(service["ports"]); binding != "" {
				entry = binding
			}
			addExposure(report, Exposure{AssetID: id, State: state, Ports: ports, EntryPoint: entry, Authentication: "unknown", TLS: "unknown", Confidence: "high", EvidenceIDs: []string{structuredEvidence(path, 1, "compose-port")}})
		}
		for dependency := range asStringMapKeys(service["depends_on"]) {
			addStructuredEdge(report, Edge{From: id, To: "compose:" + path + ":service:" + dependency, Relation: "depends-on", Verdict: "configured", Confidence: "high", ObservationType: "configured", EvidenceSource: path, EvidenceID: structuredEvidence(path, 1, "compose-dependency")})
		}
		for _, dependency := range stringSlice(service["depends_on"]) {
			addStructuredEdge(report, Edge{From: id, To: "compose:" + path + ":service:" + dependency, Relation: "depends-on", Verdict: "configured", Confidence: "high", ObservationType: "configured", EvidenceSource: path, EvidenceID: structuredEvidence(path, 1, "compose-dependency")})
		}
		collectComposeRoutes(report, path, name, id, service)
	}
}

// collectComposeRoutes recognizes the small, declarative route surface used by
// GoDoxy-style Docker labels. It deliberately reads labels only: it never
// contacts Docker, resolves DNS, or treats a configured alias as proof of
// reachability.
func collectComposeRoutes(report *Report, path, serviceName, serviceAssetID string, service map[string]any) {
	labels := routeLabels(service["labels"])
	hosts := routeValues(labels, "proxy.aliases", "proxy.host", "proxy.hosts", "proxy.domain", "proxy.domains", "route.host", "route.hosts")
	if len(hosts) == 0 {
		// GoDoxy falls back to the Compose container name when aliases are not
		// declared. Keep that fallback explicit: it is configuration evidence,
		// not a DNS or reachability check.
		hosts = routeValues(map[string]string{"host": stringValue(service["container_name"])}, "host")
		if len(hosts) == 0 {
			hosts = []string{serviceName}
		}
	}
	for index, host := range hosts {
		group := routeLabelGroup(labels, index+1)
		ports := collectPorts(firstRouteValue(group, "port", "ports", "proxy.port", "proxy.ports", "route.port", "route.ports"))
		if len(ports) == 0 {
			ports = collectPorts(service["ports"])
		}
		protocol := strings.ToLower(firstRouteValue(group, "protocol", "scheme", "proxy.protocol", "proxy.scheme", "route.protocol", "route.scheme"))
		if protocol == "" {
			protocol = "http"
		}
		target := firstRouteValue(group, "target", "upstream", "host", "proxy.target", "proxy.upstream", "route.target", "route.upstream")
		if target == "" {
			target = serviceName
		}
		pathValue := firstRouteValue(group, "path", "url_path", "proxy.path", "route.path")
		authentication := routeAuthentication(group)
		tls := routeTLS(group, protocol)
		middlewares := routeMiddlewares(group)
		routeID := "route:compose:" + path + ":service:" + serviceName + ":" + routeSafeID(host+"\x00"+pathValue+"\x00"+strconv.Itoa(index))
		asset := Asset{
			ID: routeID, Name: serviceName, Kind: "route", Provider: "godoxy-compatible-proxy", Format: "compose-labels",
			Service: serviceName, Repository: report.Root, SourcePath: path, SourceLine: 1, Owner: "unowned",
			Labels:      map[string]string{"host": host, "target": target, "protocol": protocol},
			EvidenceIDs: []string{structuredEvidence(path, 1, "proxy-route")},
		}
		addAsset(report, asset)
		addStructuredEdge(report, Edge{From: routeID, To: serviceAssetID, Relation: "routes-to", Verdict: "configured", Confidence: "high", ObservationType: "configured", EvidenceSource: path, EvidenceID: structuredEvidence(path, 1, "proxy-route-target")})
		addRoute(report, Route{
			ID: routeID, Name: serviceName, Host: host, Path: pathValue, Target: target, Protocol: protocol, Ports: ports,
			Authentication: authentication, TLS: tls, Middlewares: middlewares, ExposureState: ExposureConfiguredPublic,
			Confidence: "high", SourcePath: path, SourceLine: 1, AssetID: routeID, EvidenceIDs: []string{structuredEvidence(path, 1, "proxy-route")},
		})
	}
}

func collectKubernetesIngressRoutes(report *Report, path, namespace string, document int, root map[string]any) {
	spec, _ := asStringMap(root["spec"])
	tlsConfigured := spec["tls"] != nil
	rules, _ := spec["rules"].([]any)
	for ruleIndex, rawRule := range rules {
		rule, ok := asStringMap(rawRule)
		if !ok {
			continue
		}
		host := stringValue(rule["host"])
		httpSpec, _ := asStringMap(rule["http"])
		paths, _ := httpSpec["paths"].([]any)
		if len(paths) == 0 {
			paths = []any{map[string]any{"backend": rule["backend"]}}
		}
		for pathIndex, rawPath := range paths {
			pathSpec, _ := asStringMap(rawPath)
			backend, _ := asStringMap(pathSpec["backend"])
			serviceName, port := kubernetesBackend(backend)
			if serviceName == "" && host == "" {
				continue
			}
			pathValue := stringValue(pathSpec["path"])
			if pathValue == "" {
				pathValue = "/"
			}
			routeID := "route:k8s:" + namespace + ":" + filepath.Base(path) + ":doc:" + strconv.Itoa(document) + ":" + strconv.Itoa(ruleIndex) + ":" + strconv.Itoa(pathIndex)
			protocol := "http"
			if tlsConfigured {
				protocol = "https"
			}
			labels := map[string]string{"host": host, "path": pathValue, "protocol": protocol}
			addAsset(report, Asset{ID: routeID, Name: firstNonEmpty(host, pathValue), Kind: "route", Provider: "kubernetes", Format: "yaml", Repository: report.Root, SourcePath: path, SourceLine: 1, Owner: "unowned", Labels: labels, EvidenceIDs: []string{structuredEvidence(path, 1, "k8s-ingress-route")}})
			if serviceName != "" {
				serviceID := "k8s:" + namespace + ":service:" + serviceName
				addStructuredEdge(report, Edge{From: routeID, To: serviceID, Relation: "routes-to", Verdict: "configured", Confidence: "high", ObservationType: "configured", EvidenceSource: path, EvidenceID: structuredEvidence(path, 1, "k8s-ingress-route-target")})
			}
			route := Route{ID: routeID, Name: firstNonEmpty(host, pathValue), Host: host, Path: pathValue, Target: serviceName, Protocol: protocol, Authentication: "unknown", TLS: "absent", ExposureState: ExposureConfiguredPublic, Confidence: "high", SourcePath: path, SourceLine: 1, AssetID: routeID, EvidenceIDs: []string{structuredEvidence(path, 1, "k8s-ingress-route")}}
			if tlsConfigured {
				route.TLS = "configured"
			}
			if port > 0 {
				route.Ports = []int{port}
			}
			addRoute(report, route)
		}
	}
}

func collectRouteConfiguration(report *Report, path string, root map[string]any) {
	routes, ok := asStringMap(root["routes"])
	if !ok {
		return
	}
	for name, raw := range routes {
		config, ok := asStringMap(raw)
		if !ok {
			continue
		}
		values := routeLabels(config)
		hosts := routeValues(values, "host", "hosts", "domain", "domains", "aliases")
		if len(hosts) == 0 {
			continue
		}
		target := firstRouteValue(values, "target", "upstream", "service", "backend", "container")
		ports := collectPorts(firstRouteValue(values, "port", "ports", "target_port", "upstream_port"))
		if len(ports) == 0 {
			ports = collectPorts(target)
		}
		protocol := strings.ToLower(firstRouteValue(values, "protocol", "scheme"))
		if protocol == "" {
			protocol = "http"
		}
		pathValue := firstRouteValue(values, "path", "url_path")
		if pathValue == "" {
			pathValue = "/"
		}
		for _, host := range hosts {
			routeID := "route:file:" + path + ":" + name + ":" + routeSafeID(host+"\x00"+pathValue)
			asset := Asset{ID: routeID, Name: name, Kind: "route", Provider: "godoxy-compatible-proxy", Format: "yaml", Repository: report.Root, SourcePath: path, SourceLine: 1, Owner: "unowned", Labels: map[string]string{"host": host, "target": target, "protocol": protocol}, EvidenceIDs: []string{structuredEvidence(path, 1, "route-file")}}
			addAsset(report, asset)
			addRoute(report, Route{ID: routeID, Name: name, Host: host, Path: pathValue, Target: target, Protocol: protocol, Ports: ports, Authentication: routeAuthentication(values), TLS: routeTLS(values, protocol), Middlewares: routeMiddlewares(values), ExposureState: ExposureConfiguredPublic, Confidence: "medium", SourcePath: path, SourceLine: 1, AssetID: routeID, EvidenceIDs: []string{structuredEvidence(path, 1, "route-file")}})
		}
	}
}

func addRoute(report *Report, route Route) {
	if len(report.Routes) >= maxStructuredRoutes {
		return
	}
	for _, existing := range report.Routes {
		if existing.ID == route.ID {
			return
		}
	}
	report.Routes = append(report.Routes, route)
	state := route.ExposureState
	if state == "" {
		state = ExposurePotential
	}
	addExposure(report, Exposure{AssetID: route.AssetID, State: state, Protocol: route.Protocol, Ports: route.Ports, EntryPoint: route.Host + route.Path, Authentication: route.Authentication, TLS: route.TLS, Confidence: route.Confidence, EvidenceIDs: route.EvidenceIDs})
}

func routeLabels(value any) map[string]string {
	result := map[string]string{}
	if mapped, ok := asStringMap(value); ok {
		for key, item := range mapped {
			result[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(stringValue(item))
		}
	}
	for _, item := range stringSlice(value) {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			result[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
		}
	}
	return result
}

// routeLabelGroup overlays GoDoxy's indexed proxy labels (for example,
// proxy.#1.port and proxy.#1.scheme) onto the common label set for one alias.
// The analyzer intentionally keeps the parser flat; it only needs the
// route-facing leaves and never interprets middleware behavior.
func routeLabelGroup(values map[string]string, index int) map[string]string {
	group := make(map[string]string, len(values)+8)
	for key, value := range values {
		group[key] = value
	}
	prefix := "proxy.#" + strconv.Itoa(index) + "."
	for key, value := range values {
		if strings.HasPrefix(key, prefix) {
			group[strings.TrimPrefix(key, prefix)] = value
		}
	}
	return group
}

func routeValues(values map[string]string, keys ...string) []string {
	seen := map[string]bool{}
	var result []string
	for _, key := range keys {
		for _, value := range strings.FieldsFunc(values[strings.ToLower(key)], func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' }) {
			value = strings.Trim(strings.TrimSpace(value), "[]\"'")
			if value != "" && !seen[value] {
				seen[value] = true
				result = append(result, value)
			}
		}
	}
	return result
}

func firstRouteValue(values map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(values[strings.ToLower(key)]); value != "" {
			return value
		}
	}
	return ""
}

func routeAuthentication(values map[string]string) string {
	for _, key := range []string{"proxy.auth", "proxy.authentication", "route.auth", "route.authentication", "auth", "authentication", "forwardauth"} {
		if value := strings.TrimSpace(values[strings.ToLower(key)]); value != "" && !strings.EqualFold(value, "false") {
			return "configured"
		}
	}
	return "unknown"
}

func routeTLS(values map[string]string, protocol string) string {
	for _, key := range []string{"proxy.tls", "proxy.tls_termination", "route.tls", "route.tls_termination", "tls", "tls_termination", "https"} {
		if value := strings.TrimSpace(values[strings.ToLower(key)]); value != "" {
			if strings.EqualFold(value, "false") || strings.EqualFold(value, "off") {
				return "absent"
			}
			return "configured"
		}
	}
	if strings.EqualFold(protocol, "https") {
		return "configured"
	}
	if strings.EqualFold(protocol, "http") {
		return "absent"
	}
	return "unknown"
}

func routeMiddlewares(values map[string]string) []string {
	seen := map[string]bool{}
	var result []string
	add := func(value string) {
		for _, item := range routeValues(map[string]string{"value": value}, "value") {
			if !seen[item] {
				seen[item] = true
				result = append(result, item)
			}
		}
	}
	add(firstRouteValue(values, "proxy.middlewares", "route.middlewares", "middlewares", "middleware"))
	for key := range values {
		for _, prefix := range []string{"proxy.middlewares.", "route.middlewares.", "middlewares."} {
			if strings.HasPrefix(key, prefix) {
				name := strings.TrimPrefix(key, prefix)
				if dot := strings.IndexByte(name, '.'); dot >= 0 {
					name = name[:dot]
				}
				if name != "" && !seen[name] {
					seen[name] = true
					result = append(result, name)
				}
			}
		}
	}
	return result
}

func routeSafeID(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:6])
}

func kubernetesBackend(backend map[string]any) (string, int) {
	service, _ := asStringMap(backend["service"])
	name := stringValue(service["name"])
	if name == "" {
		name = stringValue(backend["serviceName"])
	}
	port := 0
	portSpec, _ := asStringMap(service["port"])
	if value := stringValue(portSpec["number"]); value != "" {
		port, _ = strconv.Atoi(value)
	}
	if port == 0 {
		port, _ = strconv.Atoi(stringValue(portSpec["name"]))
	}
	if port == 0 {
		port, _ = strconv.Atoi(stringValue(backend["servicePort"]))
	}
	return name, port
}

func collectKubernetes(report *Report, path string, root map[string]any, document int) {
	metadata, _ := asStringMap(root["metadata"])
	name := stringValue(metadata["name"])
	if name == "" {
		return
	}
	namespace := stringValue(metadata["namespace"])
	if namespace == "" {
		namespace = "default"
	}
	kind := strings.ToLower(stringValue(root["kind"]))
	canonicalKind := kind
	id := "k8s:" + namespace + ":" + canonicalKind + ":" + name
	labels := map[string]string{"identity": id}
	spec, _ := asStringMap(root["spec"])
	template, _ := asStringMap(spec["template"])
	templateSpec, _ := asStringMap(template["spec"])
	if len(templateSpec) == 0 {
		templateSpec = spec
	}
	if boolValue(templateSpec["hostNetwork"]) {
		labels["host_network"] = "true"
	}
	if boolValue(templateSpec["hostPID"]) {
		labels["host_pid"] = "true"
	}
	if serviceAccount := stringValue(templateSpec["serviceAccountName"]); serviceAccount != "" {
		serviceAccountID := "k8s:" + namespace + ":serviceaccount:" + serviceAccount
		addAsset(report, Asset{ID: serviceAccountID, Name: serviceAccount, Kind: "serviceaccount", Provider: "kubernetes", Format: "yaml", Repository: report.Root, SourcePath: path, SourceLine: 1, Owner: "unowned", EvidenceIDs: []string{structuredEvidence(path, 1, "k8s-service-account")}})
		labels["service_account"] = serviceAccount
	}
	if capabilities := stringValue(templateSpec["capabilities"]); capabilities != "" && capabilities != "<nil>" {
		labels["capabilities"] = capabilities
	}
	if hasPrivilegedContainer(templateSpec["containers"]) {
		labels["privileged"] = "true"
	}
	if containsValue(templateSpec["volumes"], "hostPath") || containsValue(templateSpec["volumes"], "hostPath:") {
		labels["host_path"] = "true"
	}
	asset := Asset{ID: id, Name: name, Kind: kind, Provider: "kubernetes", Format: "yaml", Repository: report.Root, SourcePath: path, SourceLine: 1, Owner: "unowned", Labels: labels, EvidenceIDs: []string{structuredEvidence(path, 1, "k8s-"+kind)}}
	addAsset(report, asset)
	switch kind {
	case "service":
		typeName := strings.ToLower(stringValue(spec["type"]))
		if typeName == "loadbalancer" || typeName == "nodeport" {
			addExposure(report, Exposure{AssetID: id, State: ExposureConfiguredPublic, EntryPoint: typeName, Authentication: "unknown", TLS: "unknown", Confidence: "medium", EvidenceIDs: []string{structuredEvidence(path, 1, "k8s-service-exposure")}})
		}
	case "ingress":
		addExposure(report, Exposure{AssetID: id, State: ExposureConfiguredPublic, Protocol: "http", EntryPoint: "ingress", Authentication: "unknown", TLS: "unknown", Confidence: "high", EvidenceIDs: []string{structuredEvidence(path, 1, "k8s-ingress")}})
		collectKubernetesIngressRoutes(report, path, namespace, document, root)
		for _, serviceName := range kubernetesBackendServiceNames(root) {
			if serviceName != "" {
				addStructuredEdge(report, Edge{From: id, To: "k8s:" + namespace + ":service:" + serviceName, Relation: "routes-to", Verdict: "configured", Confidence: "high", ObservationType: "configured", EvidenceSource: path, EvidenceID: structuredEvidence(path, 1, "k8s-ingress-backend")})
			}
		}
	case "deployment", "statefulset", "daemonset", "pod":
		if serviceAccount := stringValue(templateSpec["serviceAccountName"]); serviceAccount != "" {
			addStructuredEdge(report, Edge{From: id, To: "k8s:" + namespace + ":serviceaccount:" + serviceAccount, Relation: "uses", Verdict: "configured", Confidence: "high", ObservationType: "configured", EvidenceSource: path, EvidenceID: structuredEvidence(path, 1, "k8s-service-account")})
		}
	}
	if templateMetadata, ok := asStringMap(template["metadata"]); ok {
		if labelMap, ok := asStringMap(templateMetadata["labels"]); ok {
			for key, value := range labelMap {
				labels["label:"+key] = stringValue(value)
			}
		}
	}
	if kind == "service" {
		if selector, ok := asStringMap(spec["selector"]); ok {
			for key, value := range selector {
				labels["selector:"+key] = stringValue(value)
			}
		}
	}
	// The asset was added before these labels were discovered; update the
	// canonical copy without manufacturing a cross-file relationship.
	for index := range report.Assets {
		if report.Assets[index].ID == id {
			report.Assets[index].Labels = labels
			for nodeIndex := range report.Nodes {
				if report.Nodes[nodeIndex].ID == id {
					report.Nodes[nodeIndex].Labels = labels
				}
			}
			break
		}
	}
	_ = document
}

func collectWorkflow(report *Report, path string, root map[string]any) {
	workflowID := "github-actions:" + path
	addAsset(report, Asset{ID: workflowID, Name: filepath.Base(path), Kind: "workflow", Provider: "github-actions", Format: "yaml", Repository: report.Root, SourcePath: path, SourceLine: 1, Owner: "unowned", Labels: map[string]string{"trigger": workflowTrigger(root), "permissions": permissionLabel(root)}, EvidenceIDs: []string{structuredEvidence(path, 1, "workflow")}})
	jobs, ok := asStringMap(root["jobs"])
	if !ok {
		return
	}
	for name, raw := range jobs {
		job, _ := asStringMap(raw)
		id := workflowID + ":" + name
		labels := map[string]string{"workflow": path, "permissions": permissionLabel(root)}
		if containsMutableAction(job) {
			labels["mutable_action"] = "true"
		}
		if containsUntrustedInput(job) {
			labels["untrusted_pr_input"] = "true"
		}
		addAsset(report, Asset{ID: id, Name: name, Kind: "job", Provider: "github-actions", Format: "yaml", Repository: report.Root, SourcePath: path, SourceLine: 1, Owner: "unowned", Labels: labels, EvidenceIDs: []string{structuredEvidence(path, 1, "workflow-job")}})
		addStructuredEdge(report, Edge{From: workflowID, To: id, Relation: "runs", Verdict: "configured", Confidence: "high", ObservationType: "configured", EvidenceSource: path, EvidenceID: structuredEvidence(path, 1, "workflow-job")})
	}
}

func collectDockerfile(report *Report, path, content string) {
	id := "dockerfile:" + path
	labels := map[string]string{"identity": id}
	if match := dockerfileUserPattern.FindStringSubmatch(content); len(match) == 2 {
		labels["user"] = match[1]
	}
	if match := dockerfileExecPattern.FindStringSubmatch(content); len(match) == 3 {
		labels[strings.ToLower(match[1])] = strings.TrimSpace(match[2])
	}
	addAsset(report, Asset{ID: id, Name: filepath.Base(path), Kind: "image-build", Provider: "docker", Format: "dockerfile", Repository: report.Root, SourcePath: path, SourceLine: 1, Owner: "unowned", Labels: labels, EvidenceIDs: []string{structuredEvidence(path, 1, "dockerfile")}})
	if match := dockerfileExposePattern.FindStringSubmatch(content); len(match) == 2 {
		ports := collectPorts(strings.Fields(match[1]))
		if len(ports) > 0 {
			addExposure(report, Exposure{AssetID: id, State: ExposurePotential, Ports: ports, EntryPoint: "EXPOSE", Authentication: "unknown", TLS: "unknown", Confidence: "high", EvidenceIDs: []string{structuredEvidence(path, lineAt(content, strings.Index(content, match[0])), "dockerfile-expose")}})
		}
	}
}

func collectTerraform(report *Report, path, content string) {
	for _, match := range terraformResourcePattern.FindAllStringSubmatch(content, 64) {
		if len(match) != 3 {
			continue
		}
		name := match[1] + "." + match[2]
		id := "terraform:" + path + ":resource:" + name
		addAsset(report, Asset{ID: id, Name: name, Kind: "resource", Provider: match[1], Format: "terraform", Repository: report.Root, SourcePath: path, SourceLine: lineAt(content, strings.Index(content, match[0])), Owner: "unowned", EvidenceIDs: []string{structuredEvidence(path, lineAt(content, strings.Index(content, match[0])), "terraform-resource")}})
	}
}

func addAsset(report *Report, asset Asset) {
	if len(report.Assets) >= maxStructuredAssets {
		return
	}
	for _, existing := range report.Assets {
		if existing.ID == asset.ID {
			return
		}
	}
	report.Assets = append(report.Assets, asset)
	report.Coverage.StructuredNodes++
	if len(report.Nodes) < maxStructuredAssets+1 {
		report.Nodes = append(report.Nodes, Node{ID: asset.ID, Label: asset.Name, Kind: "asset", Path: asset.SourcePath, Labels: asset.Labels, EvidenceIDs: asset.EvidenceIDs})
	}
}

func addExposure(report *Report, exposure Exposure) {
	for _, existing := range report.Exposures {
		if existing.AssetID == exposure.AssetID && existing.EntryPoint == exposure.EntryPoint {
			return
		}
	}
	report.Exposures = append(report.Exposures, exposure)
}

func addStructuredEdge(report *Report, edge Edge) {
	if report.Coverage.StructuredEdges >= maxStructuredEdges {
		return
	}
	for _, existing := range report.Edges {
		if existing.From == edge.From && existing.To == edge.To && existing.Relation == edge.Relation {
			return
		}
	}
	report.Edges = append(report.Edges, edge)
	report.Coverage.StructuredEdges++
}

func structuredEvidence(path string, line int, kind string) string {
	sum := sha256.Sum256([]byte(path + "\x00" + strconv.Itoa(line) + "\x00" + kind))
	return "evidence:" + hex.EncodeToString(sum[:6])
}

func isComposeCandidate(path string) bool {
	lower := strings.ToLower(filepath.ToSlash(path))
	base := strings.ToLower(filepath.Base(path))
	return strings.Contains(base, "compose") || strings.HasSuffix(base, "docker-compose.yml") || strings.HasSuffix(base, "docker-compose.yaml") || strings.Contains(lower, "/compose/")
}

func isKubernetesCandidate(path string) bool {
	lower := strings.ToLower(filepath.ToSlash(path))
	return strings.Contains(lower, "k8s") || strings.Contains(lower, "kubernetes") || strings.Contains(lower, "helm") || strings.Contains(lower, "deployment") || strings.Contains(lower, "statefulset") || strings.Contains(lower, "daemonset") || strings.Contains(lower, "ingress")
}

func isRouteCandidate(path string) bool {
	lower := strings.ToLower(filepath.ToSlash(path))
	base := strings.ToLower(filepath.Base(path))
	return strings.Contains(base, "route") || strings.Contains(base, "proxy") || base == "config.yml" || base == "config.yaml" || strings.Contains(lower, "/gateway/") || strings.Contains(lower, "/routes/")
}

func asStringMap(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		return typed, true
	case map[any]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if text, ok := key.(string); ok {
				out[text] = item
			}
		}
		return out, len(out) > 0
	default:
		return nil, false
	}
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return fmt.Sprint(value)
}

func boolValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
}

func asStringMapKeys(value any) map[string]any {
	result, _ := asStringMap(value)
	return result
}

func containsValue(value any, needle string) bool {
	if strings.Contains(strings.ToLower(stringValue(value)), strings.ToLower(needle)) {
		return true
	}
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			if containsValue(item, needle) {
				return true
			}
		}
	case []string:
		for _, item := range typed {
			if strings.Contains(strings.ToLower(item), strings.ToLower(needle)) {
				return true
			}
		}
	case map[any]any, map[string]any:
		mapped, _ := asStringMap(typed)
		for key, item := range mapped {
			if strings.Contains(strings.ToLower(key), strings.ToLower(needle)) || containsValue(item, needle) {
				return true
			}
		}
	}
	return false
}

func containsString(value, needle string) bool {
	return strings.Contains(strings.ToLower(value), strings.ToLower(needle))
}

func collectPorts(value any) []int {
	seen := map[int]bool{}
	var out []int
	var collect func(any)
	collect = func(item any) {
		switch typed := item.(type) {
		case []any:
			for _, child := range typed {
				collect(child)
			}
		case []string:
			for _, child := range typed {
				collect(child)
			}
		case int:
			if typed > 0 && typed < 65536 && !seen[typed] {
				seen[typed] = true
				out = append(out, typed)
			}
		case string:
			field := strings.TrimSpace(typed)
			field = strings.TrimSuffix(field, "/tcp")
			field = strings.TrimSuffix(field, "/udp")
			parts := strings.Split(field, ":")
			if len(parts) >= 2 {
				field = parts[len(parts)-2]
				if field == "" {
					field = parts[len(parts)-1]
				}
			} else if len(parts) == 1 {
				field = parts[0]
			}
			if port, err := strconv.Atoi(field); err == nil && port > 0 && port < 65536 && !seen[port] {
				seen[port] = true
				out = append(out, port)
			}
		case map[any]any, map[string]any:
			mapped, _ := asStringMap(typed)
			collect(mapped["target"])
			collect(mapped["published"])
			collect(mapped["port"])
		}
	}
	collect(value)
	sort.Ints(out)
	return out
}

func firstHostBinding(value any) string {
	if list, ok := value.([]any); ok {
		for _, item := range list {
			text := stringValue(item)
			if strings.Contains(text, ":") {
				return text
			}
		}
	}
	if list, ok := value.([]string); ok {
		for _, item := range list {
			if strings.Contains(item, ":") {
				return item
			}
		}
	}
	return ""
}

func joinInts(values []int) string {
	parts := make([]string, len(values))
	for index, value := range values {
		parts[index] = strconv.Itoa(value)
	}
	return strings.Join(parts, ",")
}

func structuredDepth(value any, depth int) int {
	if depth > maxStructuredDepth {
		return depth
	}
	switch typed := value.(type) {
	case []any:
		max := depth
		for _, item := range typed {
			if current := structuredDepth(item, depth+1); current > max {
				max = current
			}
		}
		return max
	case map[any]any:
		max := depth
		for key, item := range typed {
			if current := structuredDepth(key, depth+1); current > max {
				max = current
			}
			if current := structuredDepth(item, depth+1); current > max {
				max = current
			}
		}
		return max
	case map[string]any:
		max := depth
		for key, item := range typed {
			if current := structuredDepth(key, depth+1); current > max {
				max = current
			}
			if current := structuredDepth(item, depth+1); current > max {
				max = current
			}
		}
		return max
	default:
		return depth
	}
}

func hasPrivilegedContainer(value any) bool {
	return containsValue(value, "privileged") && containsValue(value, "true")
}

func kubernetesBackendServiceNames(root map[string]any) []string {
	spec, _ := asStringMap(root["spec"])
	rules := spec["rules"]
	seen := map[string]bool{}
	var result []string
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case map[any]any, map[string]any:
			mapped, _ := asStringMap(typed)
			if serviceName := stringValue(mapped["serviceName"]); serviceName != "" && !seen[serviceName] {
				seen[serviceName] = true
				result = append(result, serviceName)
			}
			if service, ok := asStringMap(mapped["service"]); ok {
				if serviceName := stringValue(service["name"]); serviceName != "" && !seen[serviceName] {
					seen[serviceName] = true
					result = append(result, serviceName)
				}
			}
			for _, child := range mapped {
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(rules)
	sort.Strings(result)
	return result
}

func stringSlice(value any) []string {
	var result []string
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			if text := stringValue(item); text != "" {
				result = append(result, text)
			}
		}
	case []string:
		result = append(result, typed...)
	}
	return result
}

func joinStructuredAssets(report *Report) {
	for _, service := range report.Assets {
		if service.Provider != "kubernetes" || service.Kind != "service" {
			continue
		}
		for _, workload := range report.Assets {
			if workload.Provider != "kubernetes" || (workload.Kind != "deployment" && workload.Kind != "statefulset" && workload.Kind != "daemonset" && workload.Kind != "pod") {
				continue
			}
			if strings.HasPrefix(service.ID, "k8s:") && strings.HasPrefix(workload.ID, "k8s:") && strings.Split(service.ID, ":")[1] == strings.Split(workload.ID, ":")[1] && selectorMatches(service.Labels, workload.Labels) {
				addStructuredEdge(report, Edge{From: service.ID, To: workload.ID, Relation: "selects", Verdict: "configured", Confidence: "high", ObservationType: "configured", EvidenceSource: service.SourcePath, EvidenceID: structuredEvidence(service.SourcePath, service.SourceLine, "k8s-selector")})
			}
		}
	}
}

func selectorMatches(selector, labels map[string]string) bool {
	found := false
	for key, value := range selector {
		if !strings.HasPrefix(key, "selector:") {
			continue
		}
		found = true
		if labels["label:"+strings.TrimPrefix(key, "selector:")] != value {
			return false
		}
	}
	return found
}

func finalizeStructuredEdges(report *Report) {
	known := map[string]bool{}
	for _, node := range report.Nodes {
		known[node.ID] = true
	}
	filtered := report.Edges[:0]
	for _, edge := range report.Edges {
		if known[edge.From] && known[edge.To] {
			filtered = append(filtered, edge)
		}
	}
	report.Edges = filtered
	if len(report.Edges) > maxStructuredEdges {
		report.Edges = report.Edges[:maxStructuredEdges]
	}
	report.Coverage.StructuredEdges = len(report.Edges)
}

func workflowTrigger(root map[string]any) string {
	if value := root["on"]; value != nil {
		return strings.TrimSpace(stringValue(value))
	}
	if value := root["true"]; value != nil {
		return strings.TrimSpace(stringValue(value))
	}
	return "unknown"
}

func permissionLabel(root map[string]any) string {
	value := root["permissions"]
	if value == nil {
		return "default/unknown"
	}
	return strings.TrimSpace(stringValue(value))
}

func containsMutableAction(value any) bool {
	text := strings.ToLower(stringValue(value))
	return strings.Contains(text, "@main") || strings.Contains(text, "@master") || strings.Contains(text, "@latest")
}

func containsUntrustedInput(value any) bool {
	text := strings.ToLower(stringValue(value))
	return strings.Contains(text, "pull_request") || strings.Contains(text, "github.head_ref") || strings.Contains(text, "github.event")
}
