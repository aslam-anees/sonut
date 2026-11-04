package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/aistatine/sonut/internal/sec"
	"github.com/aistatine/sonut/internal/sec/processinsight"
	"github.com/aistatine/sonut/internal/security/sensor"
)

type secScanResultMsg struct {
	seq        int
	tab        secTab
	report     sec.Report
	reportPath string
	summary    string
	err        error
}

type secProcessResultMsg struct {
	target processinsight.Target
	result processinsight.Result
	err    error
}

// startSecCommand implements the native security posture workflow:
//
//	/sec         scan, persist report.json, and open the TUI
//	/sec rescan  repeat the bounded read-only full scan
//	/sec diff    Git-changed files only (codex-security diff scan port)
//	/sec open    reopen the in-memory or persisted report in the TUI
//	/sec activity open the unified view on live session egress
//	/sec atlas  open the live world network atlas
//	/sec tools   open the unified view on outbound-capable tool surfaces
//	/sec signals open the unified view on agent telemetry and behavior triage
//	/sec help    explain the trust and coverage boundary
func (m model) startSecCommand(argText string) (model, tea.Cmd) {
	action := strings.ToLower(strings.TrimSpace(argText))
	switch action {
	case "help", "-h", "--help":
		text := renderCommandCardTranscript(commandCard{
			Title:   "Security posture",
			Summary: []string{"agentless", "local only", "read-only scan", "native TUI"},
			Sections: []commandCardSection{
				{
					Title: "What /sec checks",
					Lines: []string{
						"secrets and sensitive files",
						"public exposure and unsafe transport",
						"container, CI, identity, and agent permissions",
						"dangerous sinks: pickle, unsafe yaml, shell=True, eval, XSS HTML",
						"supply-chain pins, CI script injection, foundational controls",
						"toxic combinations backed by connected evidence",
						"SECURITY.md policy excerpt + SARIF/markdown exports",
						"live session egress decisions and outbound-capable tool surfaces",
						"normalized agent telemetry and high-recall behavior triage",
						"opt-in Linux eBPF sensor status and bounded network enforcement",
						"runtime process, port, name, and executable explanation (never full environments)",
					},
				},
				{
					Title: "Modes",
					Lines: []string{
						"/sec or /sec rescan — full workspace inventory",
						"/sec diff — only Git-changed files (fast PR / working-tree review)",
						"/sec atlas — live world view of guarded egress and local networks",
						"/sec routes — inspect configured proxy and ingress mappings",
						"/sec sensor status|start|stop|help — explicit human-only sensor lifecycle",
						"/sec processes — explain the current @code process",
						"/sec process pid|port|name|file <value> — inspect one bounded runtime target",
					},
				},
				{
					Title: "Trust boundary",
					Lines: []string{
						"does not run project code or query live cloud accounts",
						"does not upload workspace content",
						"does not store detected secret values",
						"does not claim exploitation or reachability without evidence",
						"SECURITY.md is treated as untrusted policy data",
					},
				},
				{
					Title: "AI advisor",
					Lines: []string{
						"runs inside the /sec TUI with the configured model",
						"receives the bounded redacted report, never raw workspace files",
						"cannot run tools or silently change project files",
					},
				},
			},
			Actions: []string{"/sec", "/sec diff", "/sec routes", "/sec processes", "/sec process port 3000", "/sec sensor status"},
		})
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: text})
		return m, nil
	case "sensor", "sensor status", "sensor start", "sensor stop", "sensor help":
		return m.startSecSensorCommand(strings.TrimSpace(strings.TrimPrefix(action, "sensor")))
	case "", "scan", "rescan":
		return m.runSecScan(sec.DefaultOptions())
	case "diff", "pr", "changes":
		return m.runSecScan(sec.DiffOptions())
	case "activity", "egress":
		return m.runSecScanForTab(sec.DefaultOptions(), secTabActivity)
	case "atlas", "map", "network":
		return m.runSecScanForTab(sec.DefaultOptions(), secTabAtlas)
	case "routes", "proxy", "exposure":
		return m.runSecScanForTab(sec.DefaultOptions(), secTabRoutes)
	case "tools", "surfaces":
		return m.runSecScanForTab(sec.DefaultOptions(), secTabSurfaces)
	case "signals", "agent", "telemetry":
		return m.runSecScanForTab(sec.DefaultOptions(), secTabSignals)
	case "processes":
		return m.runSecProcess(processinsight.PID(strconv.Itoa(os.Getpid())))
	case "process":
		return m.appendSecProcessUsage(), nil
	case "open":
		if m.secView.report.Version == sec.ReportVersion {
			m.secView.active = true
			m.chatScrollOffset = 0
			m.clearComposer()
			m.clearSuggestions()
			return m, nil
		}
		reportPath := secReportPath(m.cwd)
		report, err := sec.ReadReport(reportPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return m.runSecScan(sec.DefaultOptions())
			}
			return m.appendSecError("report unavailable", err), nil
		}
		return m.openSecView(report, reportPath), nil
	default:
		if strings.HasPrefix(action, "process ") {
			return m.startSecProcessTarget(strings.TrimSpace(strings.TrimPrefix(action, "process ")))
		}
		text := renderCommandCardTranscript(commandCard{
			Title:   "Security posture",
			Summary: []string{"unknown action"},
			Sections: []commandCardSection{{
				Title: "Usage",
				Lines: []string{"/sec", "/sec rescan", "/sec diff", "/sec routes", "/sec processes", "/sec process pid|port|name|file <value>", "/sec open", "/sec atlas", "/sec activity", "/sec tools", "/sec signals", "/sec sensor status", "/sec help"},
			}},
		})
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: text})
		return m, nil
	}
}

func (m model) startSecProcessTarget(spec string) (model, tea.Cmd) {
	parts := strings.Fields(spec)
	if len(parts) < 2 {
		return m.appendSecProcessUsage(), nil
	}
	kind := strings.ToLower(parts[0])
	value := strings.Trim(strings.Join(parts[1:], " "), "\"'")
	if kind != "file" && len(parts) != 2 {
		return m.appendSecProcessUsage(), nil
	}
	var target processinsight.Target
	switch kind {
	case "pid":
		target = processinsight.PID(value)
	case "port":
		target = processinsight.Port(value)
	case "name":
		target = processinsight.Name(value)
	case "file":
		target = processinsight.File(value)
	default:
		return m.appendSecProcessUsage(), nil
	}
	return m.runSecProcess(target)
}

func (m model) runSecProcess(target processinsight.Target) (model, tea.Cmd) {
	targetValue := target
	return m, func() tea.Msg {
		result, err := processinsight.Inspect(context.Background(), targetValue)
		return secProcessResultMsg{target: targetValue, result: result, err: err}
	}
}

func (m model) appendSecProcessUsage() model {
	text := renderCommandCardTranscript(commandCard{
		Title:   "Process insight",
		Summary: []string{"bounded runtime observation", "read-only", "no elevation"},
		Sections: []commandCardSection{{Title: "Usage", Lines: []string{
			"/sec processes — explain the current @code process",
			"/sec process pid <pid>",
			"/sec process port <port>",
			"/sec process name <name>",
			"/sec process file <path>",
		}}},
		Actions: []string{"/sec processes", "/sec process pid " + strconv.Itoa(os.Getpid())},
	})
	m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: text})
	return m
}

func (m model) applySecProcessResult(msg secProcessResultMsg) model {
	if msg.err != nil {
		return m.appendSecError("process insight failed", msg.err)
	}
	result := msg.result
	summary := []string{"runtime observation", string(msg.target.Kind) + " " + msg.target.Value}
	sections := []commandCardSection{}
	if result.Source != "" {
		sections = append(sections, commandCardSection{Title: "Collector", Lines: []string{"source: " + result.Source, "observed: " + result.ObservedAt.Local().Format("2006-01-02 15:04:05")}})
	}
	if result.Process != nil {
		process := result.Process
		lines := []string{
			fmt.Sprintf("PID %d · parent %d · %s", process.PID, process.PPID, nonEmpty(process.User, "user unknown")),
			"command: " + nonEmpty(process.Command, "unknown"),
			"arguments: " + nonEmpty(process.Arguments, "none reported"),
			"executable: " + nonEmpty(process.Executable, "unknown"),
			"working directory: " + nonEmpty(process.WorkingDir, "unknown"),
		}
		for _, socket := range process.Sockets {
			address := nonEmpty(socket.Address, "local")
			if socket.Port > 0 {
				address += ":" + strconv.Itoa(socket.Port)
			}
			lines = append(lines, "socket: "+nonEmpty(socket.Protocol, "network")+" "+address+" "+nonEmpty(socket.State, "observed"))
		}
		sections = append(sections, commandCardSection{Title: "Process", Lines: lines})
	}
	if len(result.Ancestry) > 0 {
		lines := make([]string, 0, len(result.Ancestry))
		for _, process := range result.Ancestry {
			lines = append(lines, fmt.Sprintf("%d ← %s", process.PID, nonEmpty(process.Command, "unknown")))
		}
		sections = append(sections, commandCardSection{Title: "Ancestry", Lines: lines})
	}
	if len(result.Children) > 0 {
		lines := make([]string, 0, len(result.Children))
		for _, process := range result.Children {
			lines = append(lines, fmt.Sprintf("%d · %s", process.PID, nonEmpty(process.Command, "unknown")))
		}
		sections = append(sections, commandCardSection{Title: "Children", Lines: lines})
	}
	if result.NotFound {
		sections = append(sections, commandCardSection{Title: "Result", Lines: []string{"No matching process was observed at inspection time."}})
	}
	if result.Unsupported {
		sections = append(sections, commandCardSection{Title: "Availability", Lines: []string{"This target is not supported by the current platform collector."}})
	}
	if result.Truncated {
		sections = append(sections, commandCardSection{Title: "Coverage", Lines: []string{"The operating-system output reached the bounded collection limit."}})
	}
	if len(result.Warnings) > 0 {
		sections = append(sections, commandCardSection{Title: "Warnings", Lines: result.Warnings})
	}
	sections = append(sections, commandCardSection{Title: "Trust boundary", Lines: []string{
		"read-only OS observation; no process control or privilege escalation",
		"environment variables and secret values are not collected",
		"snapshot is not merged into the persisted static security report",
	}})
	text := renderCommandCardTranscript(commandCard{Title: "Process insight", Summary: summary, Sections: sections, Actions: []string{"/sec processes", "/sec"}})
	m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: text})
	return m
}

func (m model) startSecSensorCommand(subcommand string) (model, tea.Cmd) {
	action := strings.ToLower(strings.TrimSpace(subcommand))
	manager := sensor.NewManager(m.cwd)
	var summary []string
	switch action {
	case "", "status":
		status := manager.Status()
		summary = append([]string{"read-only status"}, sensorStatusLines(status)...)
	case "start":
		err := manager.Start(sensor.Options{Root: m.cwd, TargetPID: os.Getpid()})
		if err != nil {
			text := renderCommandCardTranscript(commandCard{Title: "Linux eBPF sensor", Summary: []string{"not started"}, Sections: []commandCardSection{{Title: "Reason", Lines: []string{err.Error(), "The Tool Guard, sandbox, and static /sec scan remain operational."}}}, Actions: []string{"/sec sensor status"}})
			m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: text})
			return m, nil
		}
		summary = append([]string{"explicit human start accepted"}, sensorStatusLines(manager.Status())...)
	case "stop":
		if err := manager.Stop(); err != nil {
			text := renderCommandCardTranscript(commandCard{Title: "Linux eBPF sensor", Summary: []string{"not stopped"}, Sections: []commandCardSection{{Title: "Reason", Lines: []string{err.Error()}}}, Actions: []string{"/sec sensor status"}})
			m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: text})
			return m, nil
		}
		summary = append([]string{"explicit human stop requested"}, sensorStatusLines(manager.Status())...)
	case "help":
		summary = []string{"Linux-only", "opt-in", "privileged helper", "no packet payloads", "no HTTPS decryption", "network hook degrades honestly when unavailable", "agent text cannot invoke this lifecycle"}
	default:
		summary = []string{"usage: /sec sensor status | /sec sensor start | /sec sensor stop | /sec sensor help"}
	}
	text := renderCommandCardTranscript(commandCard{Title: "Linux eBPF sensor", Summary: summary, Sections: []commandCardSection{{Title: "Privilege boundary", Lines: []string{"Only the explicitly invoked atcode-sensor helper may load the bundled BPF program.", "The agent and sandboxed children receive no BPF handles or writable control channel."}}}, Actions: []string{"/sec sensor status", "/sec sensor start", "/sec sensor stop"}})
	m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: text})
	return m, nil
}

func (m model) runSecScan(options sec.Options) (model, tea.Cmd) {
	return m.runSecScanForTab(options, secTabOverview)
}

// runSecScanForTab starts the scan outside the Bubble Tea update loop so the
// transition can render immediately. The result is applied only if the view
// that requested it is still current; closing /sec while a scan is running is
// therefore safe and needs no goroutine cancellation plumbing.
func (m model) runSecScanForTab(options sec.Options, tab secTab) (model, tea.Cmd) {
	m.secScanSeq++
	seq := m.secScanSeq
	parentOffset := m.chatScrollOffset
	if m.secView.active {
		parentOffset = m.secView.parentScrollOffset
	}
	m.secView = secViewState{
		active:             true,
		transitioning:      true,
		transitionSeq:      seq,
		parentScrollOffset: parentOffset,
	}
	m.chatScrollOffset = 0
	m.clearComposer()
	m.clearSuggestions()
	m = m.clearHover()

	cwd := m.cwd
	return m, tea.Batch(
		secTransitionTickCmd(seq),
		func() tea.Msg {
			report, err := sec.Scan(cwd, options)
			if err != nil {
				return secScanResultMsg{seq: seq, tab: tab, summary: "scan failed", err: err}
			}
			artifacts, err := sec.WriteArtifacts(report)
			if err != nil {
				return secScanResultMsg{seq: seq, tab: tab, summary: "report write failed", err: err}
			}
			return secScanResultMsg{seq: seq, tab: tab, report: report, reportPath: artifacts.Report}
		},
	)
}

func (m model) applySecScanResult(msg secScanResultMsg) model {
	if !m.secView.active || !m.secView.transitioning || msg.seq != m.secView.transitionSeq {
		return m
	}
	if msg.err != nil {
		m = m.closeSecView()
		summary := msg.summary
		if summary == "" {
			summary = "scan failed"
		}
		return m.appendSecError(summary, msg.err)
	}
	m = m.openSecView(msg.report, msg.reportPath)
	if msg.tab != secTabOverview {
		m = m.setSecTab(msg.tab)
	}
	return m
}

func (m model) appendSecError(summary string, err error) model {
	text := renderCommandCardTranscript(commandCard{
		Title:   "Security posture",
		Summary: []string{summary},
		Sections: []commandCardSection{{
			Title: "Error",
			Lines: []string{err.Error()},
		}},
	})
	m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: text})
	return m
}

func countAttackPaths(report sec.Report) int {
	count := 0
	for _, finding := range report.Findings {
		if len(finding.AttackPath) > 0 {
			count++
		}
	}
	return count
}

func formatSecBytes(value int64) string {
	switch {
	case value < 1024:
		return fmt.Sprintf("%dB", value)
	case value < 1024*1024:
		return fmt.Sprintf("%.1fKB", float64(value)/1024)
	default:
		return fmt.Sprintf("%.1fMB", float64(value)/(1024*1024))
	}
}
