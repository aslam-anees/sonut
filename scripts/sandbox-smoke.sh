#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

export GOCACHE="${GOCACHE:-/tmp/sonut-go-cache}"

go test ./internal/sandbox -run 'TestPermissionProfileFromPolicyBuildsWorkspaceWriteProfile|TestPermissionProfileFromDisabledPolicyDoesNotRequirePlatformSandbox|TestSandboxManagerBuildsExecutionRequestFromProfile|TestSandboxManagerBuildsCommandPlanThroughLinuxHelper|TestSandboxManagerBuildsCommandPlanThroughWindowsRunner|TestSandboxManagerRejectsUnavailableCommandPlan|TestSandboxManagerSelectsPlatformBackend|TestSelectBackendDelegatesToSandboxManagerSelection|TestSandboxManagerFailsClosedWhenNativeRequiredAndUnavailable|TestBuildLinuxSandboxCommandArgsSerializesPermissionProfile|TestParseLinuxSandboxHelperArgs|TestBuildLinuxSandboxBwrapArgsWrapsInnerSeccompStage|TestWindowsCapabilitySIDsPersistAndReuse|TestWindowsCapabilitySIDsAreScopedByRoot|TestWindowsCapabilitySIDsForConfigSelectsReadOnlySID|TestWindowsCapabilitySIDsForConfigSelectsWritableRootSIDs|TestBuildWindowsACLPlanForWorkspaceWriteProfile|TestBuildWindowsNetworkPlanForAllowKeepsWFPNamespaceForCleanup|TestBuildWindowsNetworkPlanForDenyUsesCapabilityIdentity|TestBuildWindowsNetworkPlanFailsClosedForScoped|TestWindowsNetworkPlanHashIsStableAcrossEntryOrder|TestBuildAndParseWindowsSandboxSetupArgs|TestRunWindowsSandboxCommandRunnerRejectsInvalidArgs|TestRunWindowsSandboxSetupRejectsInvalidArgs|TestWindowsSandboxSetupMarkerRefreshesWhenProfileChanges|TestWindowsACLPlanHashIsStableAcrossEntryOrder|TestTargetBackendForPlatformBaseline|TestBackendPlanCarriesPhase0ManagerFields|TestCommandPlanCarriesSandboxMetadata|TestUnavailableFailClosedForTargetPlatforms|TestLegacySandboxEntrypointsAreExplicitAndExist|TestSelectBackendChoosesPlatformAdapterWithFallback|TestBackendBuildPlanDocumentsBestEffortIsolation|TestBackendCapabilitiesReflectDisabledPolicy'
go test ./internal/cli -run 'TestRunSandboxPolicyInspectTextAndJSON|TestRunSandboxPolicyJSONGoldenIncludesManagerBaselineFields|TestRunSandboxPolicyEffectiveTextAndJSON'
go test ./internal/tools -run 'TestRegistryRunsBashThroughSandboxEngine|TestBashToolReportsUnavailableNativeSandbox|TestBashToolBuildsWrappedSandboxExecCommand'

tmpdir="$(mktemp -d)"
cleanup() {
  rm -rf "$tmpdir"
}
trap cleanup EXIT

compile_pkg() {
  local goos="$1"
  local pkg="$2"
  local name
  name="$(basename "$pkg")"
  GOOS="$goos" GOARCH=amd64 go test -c -o "$tmpdir/${goos}-${name}.test" "$pkg"
}

build_cmd() {
  local goos="$1"
  local pkg="$2"
  local name="$3"
  GOOS="$goos" GOARCH=amd64 go build -o "$tmpdir/${goos}-${name}" "$pkg"
}

for goos in linux darwin windows; do
  compile_pkg "$goos" ./internal/sandbox
  compile_pkg "$goos" ./internal/cli
  compile_pkg "$goos" ./cmd/atcode
done
compile_pkg linux ./cmd/atcode-linux-sandbox
compile_pkg linux ./cmd/atcode-seccomp
build_cmd windows ./cmd/atcode-windows-command-runner atcode-windows-command-runner.exe
build_cmd windows ./cmd/atcode-windows-sandbox-setup atcode-windows-sandbox-setup.exe

case "$(go env GOOS)" in
  linux)
    SONUT_SANDBOX_REAL_SMOKE=1 go test ./internal/sandbox -run 'TestLinuxHelperRealSandboxSmoke|TestLinuxLandlockRealSandboxSmoke' -count=1
    ;;
  darwin)
    go test ./internal/sandbox -run TestSandboxExecProfileAllowsDevNullAndTemp -count=1
    ;;
  windows)
    SONUT_SANDBOX_REAL_SMOKE=1 \
      ATCODE_WINDOWS_COMMAND_RUNNER_EXE="$tmpdir/windows-atcode-windows-command-runner.exe" \
      ATCODE_WINDOWS_SANDBOX_SETUP_EXE="$tmpdir/windows-atcode-windows-sandbox-setup.exe" \
      go test ./internal/sandbox -run TestWindowsRestrictedTokenRealSandboxSmoke -count=1
    ;;
  *)
    echo "No real sandbox smoke is defined for this host platform."
    ;;
esac
