package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/aistatine/sonut/internal/perfbench"
)

// taskOptions configures the `atcode-perf-bench tasks` subcommand: the reproducible
// Terminal-Bench-style harness that runs @code headlessly against a task set and
// records a publishable result (model + commit + self-correct flag + date).
type taskOptions struct {
	SuitePath   string
	Model       string
	Mode        string
	SelfCorrect bool
	Binary      string
	Version     string
	Commit      string
	Output      string
	JSON        bool
	DryRun      bool
	Help        bool
}

func runTasksCommand(args []string, getenv func(string) string, stdout io.Writer, stderr io.Writer) int {
	options, err := parseTaskArgs(args, getenv)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err.Error())
		return 2
	}
	if options.Help {
		_, _ = fmt.Fprint(stdout, taskHelpText())
		return 0
	}

	set, err := perfbench.LoadTaskSet(options.SuitePath)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "[@code] Task benchmark failed: "+err.Error())
		return 1
	}

	runner := perfbench.SkipRunner
	if !options.DryRun {
		binary := strings.TrimSpace(options.Binary)
		if binary == "" {
			_, _ = fmt.Fprintln(stderr, "[@code] Task benchmark failed: --binary is required unless --dry-run is set")
			return 2
		}
		runner = perfbench.NewExecRunner(binary)
	}

	result, err := perfbench.RunTasks(context.Background(), set, perfbench.TaskConfig{
		Model:       options.Model,
		Mode:        options.Mode,
		SelfCorrect: options.SelfCorrect,
		Version:     options.Version,
		Commit:      options.Commit,
		Runner:      runner,
	})
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "[@code] Task benchmark failed: "+err.Error())
		return 1
	}

	if options.Output != "" {
		if err := writeTaskReport(options.Output, result); err != nil {
			_, _ = fmt.Fprintln(stderr, "[@code] Task benchmark failed: "+err.Error())
			return 1
		}
	}
	if options.JSON {
		if err := perfbench.WriteTaskJSON(stdout, result); err != nil {
			_, _ = fmt.Fprintln(stderr, "[@code] Task benchmark failed: "+err.Error())
			return 1
		}
		return 0
	}
	_, _ = fmt.Fprintln(stdout, perfbench.FormatTaskSummary(result))
	return 0
}

func parseTaskArgs(args []string, getenv func(string) string) (taskOptions, error) {
	getenv = compatGetenv(getenv)
	options := taskOptions{
		Version: strings.TrimSpace(getenv("ATCODE_BENCH_VERSION")),
		Commit:  strings.TrimSpace(getenv("ATCODE_BENCH_COMMIT")),
	}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		flag, inlineValue := splitFlagValue(arg)
		switch flag {
		case "--suite":
			value, next, err := readOptionValue(args, inlineValue, index, flag)
			if err != nil {
				return options, err
			}
			options.SuitePath = value
			index = next
		case "--model":
			value, next, err := readOptionValue(args, inlineValue, index, flag)
			if err != nil {
				return options, err
			}
			options.Model = value
			index = next
		case "--mode":
			value, next, err := readOptionValue(args, inlineValue, index, flag)
			if err != nil {
				return options, err
			}
			options.Mode = value
			index = next
		case "--binary":
			value, next, err := readOptionValue(args, inlineValue, index, flag)
			if err != nil {
				return options, err
			}
			options.Binary = value
			index = next
		case "--version":
			value, next, err := readOptionValue(args, inlineValue, index, flag)
			if err != nil {
				return options, err
			}
			options.Version = value
			index = next
		case "--commit":
			value, next, err := readOptionValue(args, inlineValue, index, flag)
			if err != nil {
				return options, err
			}
			options.Commit = value
			index = next
		case "--output":
			value, next, err := readOptionValue(args, inlineValue, index, flag)
			if err != nil {
				return options, err
			}
			options.Output = value
			index = next
		case "--self-correct":
			if strings.Contains(arg, "=") {
				return options, fmt.Errorf("%s does not accept a value", flag)
			}
			options.SelfCorrect = true
		case "--json":
			if strings.Contains(arg, "=") {
				return options, fmt.Errorf("%s does not accept a value", flag)
			}
			options.JSON = true
		case "--dry-run":
			if strings.Contains(arg, "=") {
				return options, fmt.Errorf("%s does not accept a value", flag)
			}
			options.DryRun = true
		case "-h", "--help":
			if strings.Contains(arg, "=") {
				return options, fmt.Errorf("%s does not accept a value", flag)
			}
			options.Help = true
		default:
			return options, fmt.Errorf("unknown option: %s", arg)
		}
	}
	if options.Help {
		return options, nil
	}
	if strings.TrimSpace(options.SuitePath) == "" {
		return options, fmt.Errorf("--suite is required")
	}
	if strings.TrimSpace(options.Model) == "" {
		return options, fmt.Errorf("--model is required")
	}
	return options, nil
}

func writeTaskReport(path string, result perfbench.TaskRunResult) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	var buffer bytes.Buffer
	if err := perfbench.WriteTaskJSON(&buffer, result); err != nil {
		return err
	}
	return os.WriteFile(path, buffer.Bytes(), 0o644)
}

func taskHelpText() string {
	return strings.Join([]string{
		"Usage: atcode-perf-bench tasks [options]",
		"",
		"Runs @code headlessly against a Terminal-Bench-style task set and records a",
		"reproducible, publishable result (model + commit + self-correct flag + date).",
		"",
		"Options:",
		"  --suite <path>      Task set JSON file (required)",
		"  --model <model>     Model to run (required); recorded with the result",
		"  --mode <name>       Exec mode preset to apply",
		"  --self-correct      Enable the post-edit verify-and-correct loop",
		"  --binary <path>     Path to the agent binary (required unless --dry-run)",
		"  --version <v>       Record the @code version (default: $ATCODE_BENCH_VERSION)",
		"  --commit <sha>      Record the @code commit (default: $ATCODE_BENCH_COMMIT)",
		"  --output <path>     Write the JSON result to path",
		"  --json              Print only the JSON result",
		"  --dry-run           Record every task as skipped without invoking the agent",
		"  -h, --help          Show this help",
		"",
		"Run twice — with and without --self-correct — to publish the delta.",
	}, "\n") + "\n"
}
