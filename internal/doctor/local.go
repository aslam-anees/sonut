package doctor

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aistatine/sonut/internal/llama"
)

// LocalRuntime describes installed local inference resources. Presence is not
// a generation benchmark; readiness is probed only when connectivity is requested.
type LocalRuntime struct {
	EnginePath string
	ModelPath  string
	Ready      bool
}

func DetectLocalRuntime(connectivity bool) *LocalRuntime {
	engine, _ := llama.FindAtcodeServer()
	model := strings.TrimSpace(os.Getenv("ATCODE_MODEL_PATH"))
	if model == "" {
		model = strings.TrimSpace(os.Getenv("SONUT_MODEL_PATH"))
	}
	if model == "" {
		model, _ = llama.GetModelPath()
	}
	if info, err := os.Stat(model); err != nil || !info.Mode().IsRegular() {
		model = ""
	}
	if engine == "" && model == "" {
		return nil
	}
	local := &LocalRuntime{EnginePath: engine, ModelPath: model}
	if connectivity {
		ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/health", llama.InternalLlamaPort), nil)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
			local.Ready = resp.StatusCode == http.StatusOK
		}
	}
	return local
}

func localRuntimeChecks(local *LocalRuntime, connectivity bool) []Check {
	engineStatus, engineMessage := StatusPass, "Local engine executable is installed; no cloud provider profile is required."
	if local.EnginePath == "" {
		engineStatus, engineMessage = StatusFail, "Local engine executable is missing; install a compatible runtime."
	}
	modelStatus, modelMessage := StatusPass, "Local model file is present. Generation quality must be checked with an actual request."
	if local.ModelPath == "" {
		modelStatus, modelMessage = StatusFail, "Local model file is missing; configure ATCODE_MODEL_PATH or run setup."
	}
	status, message := StatusWarn, "Local engine readiness was not probed. Run `at doctor --connectivity` to check; normal requests start the engine on demand."
	if connectivity {
		message = "Local engine is not ready at its loopback endpoint. Normal requests start it on demand; check startup errors if it fails to become ready."
		if local.Ready {
			status, message = StatusPass, "Local engine health endpoint is ready on loopback. This is not a generation test."
		}
	}
	return []Check{
		check("provider.config", "Local engine", engineStatus, engineMessage, map[string]any{"runtime": "local", "enginePath": local.EnginePath}),
		check("provider.model", "Local model", modelStatus, modelMessage, map[string]any{"modelPath": local.ModelPath}),
		check("provider.connectivity", "Local engine readiness", status, message, nil),
	}
}
