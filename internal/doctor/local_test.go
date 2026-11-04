package doctor

import (
	"strings"
	"testing"
)

func TestDoctorRecognizesInstalledLocalRuntimeWithoutCloudProfile(t *testing.T) {
	r := Run(Options{LocalRuntime: &LocalRuntime{EnginePath: "/local/engine", ModelPath: "/local/model.gguf"}})
	if !r.OK {
		t.Fatalf("installed local resources must not require a cloud profile: %#v", r.Checks)
	}
	if c := r.Check("provider.config"); c.Status != StatusPass || !strings.Contains(c.Message, "no cloud") {
		t.Fatalf("unexpected local configuration status: %#v", c)
	}
	if c := r.Check("provider.connectivity"); c.Status != StatusWarn {
		t.Fatal("unprobed local server must not be reported as ready")
	}
}

func TestDoctorLocalReadinessDoesNotHideMissingResources(t *testing.T) {
	r := Run(Options{Connectivity: true, LocalRuntime: &LocalRuntime{Ready: true}})
	if r.OK || r.Check("provider.config").Status != StatusFail || r.Check("provider.model").Status != StatusFail {
		t.Fatal("health readiness must not conceal missing engine/model files")
	}
	r = Run(Options{Connectivity: true, LocalRuntime: &LocalRuntime{EnginePath: "/local/engine", ModelPath: "/local/model.gguf", Ready: true}})
	if !r.OK || r.Check("provider.connectivity").Status != StatusPass {
		t.Fatal("expected installed and ready local runtime to pass")
	}
}
