package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/aistatine/sonut/internal/security"
)

func TestSecAtlasRegionPlacementIsHonest(t *testing.T) {
	known := secAtlasPointForHost("api.us-east-1.example.internal")
	if !known.located || known.label == "" {
		t.Fatalf("known cloud region should be placed: %#v", known)
	}
	southeast := secAtlasPointForHost("api.ap-southeast-1.example.internal")
	if southeast.label != "Southeast Asia" {
		t.Fatalf("southeast region matched incorrectly: %#v", southeast)
	}
	unknown := secAtlasPointForHost("api.example.com")
	if unknown.located || unknown.label != "unlocated" || unknown.y < 0.9 {
		t.Fatalf("generic host must stay on unlocated rail: %#v", unknown)
	}
}

func TestSecAtlasKnownRegionsLandOnSilhouette(t *testing.T) {
	mask := secAtlasMask(112)
	for _, host := range []string{
		"api.us-west-1.example", "api.us-east-1.example", "api.sa-east-1.example",
		"api.eu-west-1.example", "api.eu-central-1.example", "api.af-south-1.example",
		"api.ap-south-1.example", "api.ap-southeast-1.example", "api.ap-northeast-1.example",
		"api.australiaeast.example",
	} {
		point := secAtlasPointForHost(host)
		x := clampInt(int(point.x*float64(111)), 0, 111)
		y := clampInt(int(point.y*float64(len(mask)-2)), 0, len(mask)-2)
		if []rune(mask[y])[x] == ' ' {
			t.Fatalf("known region marker fell in ocean: %s at (%d,%d)", host, x, y)
		}
	}
}

func TestSecAtlasFiltersRealVerdicts(t *testing.T) {
	events := []security.Event{
		{Host: "allowed.example", Verdict: security.VerdictAllowed, Classification: security.Clean},
		{Host: "blocked.example", Verdict: security.VerdictBlocked, Classification: security.Blocked},
		{Host: "sensitive.example", Verdict: security.VerdictAllowed, Classification: security.Sensitive},
	}
	if got := len(secAtlasFilteredEvents(events, secAtlasFilterAll)); got != 3 {
		t.Fatalf("all filter returned %d events", got)
	}
	if got := secAtlasFilteredEvents(events, secAtlasFilterBlocked); len(got) != 1 || got[0].Host != "blocked.example" {
		t.Fatalf("blocked filter = %#v", got)
	}
	if got := secAtlasFilteredEvents(events, secAtlasFilterSensitive); len(got) != 2 {
		t.Fatalf("sensitive filter returned %#v", got)
	}
}

func TestSecAtlasMapAndFlowAreTerminalNative(t *testing.T) {
	events := []security.Event{{
		Timestamp: time.Unix(1, 0), Host: "service.eu-west-1.example", Tool: "web_fetch",
		Verdict: security.VerdictAllowed, Classification: security.Clean,
	}}
	rendered := ansi.Strip(strings.Join(renderSecAtlasMap(events, 100, 2, 0), "\n"))
	for _, want := range []string{"•", "●"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("atlas map missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "<html") {
		t.Fatal("atlas must remain terminal-native")
	}
}

func TestSecAtlasMapShowsLocalActiveAnchorWhenQuiet(t *testing.T) {
	rendered := ansi.Strip(strings.Join(renderSecAtlasMap(nil, 100, 0, 0), "\n"))
	if !strings.Contains(rendered, "●") {
		t.Fatalf("quiet atlas map missing local active anchor:\n%s", rendered)
	}
}

func TestSecAtlasLocalPointUsesTimezoneRegionWithoutCityPrecision(t *testing.T) {
	point := secAtlasLocalPointForZone("Asia/Kolkata", "IST", 5*60*60+30*60)
	if !point.located || point.label != "India · local timezone" {
		t.Fatalf("unexpected India local point: %#v", point)
	}
	mask := secAtlasMask(112)
	x := clampInt(int(point.x*float64(111)), 0, 111)
	y := clampInt(int(point.y*float64(len(mask)-2)), 0, len(mask)-2)
	if []rune(mask[y])[x] == ' ' {
		t.Fatalf("local India marker fell in ocean at (%d,%d)", x, y)
	}

	unknown := secAtlasLocalPointForZone("", "UTC", 0)
	if unknown.located || unknown.label != "unlocated" {
		t.Fatalf("unknown timezone should stay unlocated: %#v", unknown)
	}
}

func TestSecAtlasMaskHasWideWorldSilhouette(t *testing.T) {
	mask := secAtlasMask(100)
	if len(mask) != 27 {
		t.Fatalf("wide atlas height = %d, want 27", len(mask))
	}
	landRows := 0
	for _, row := range mask {
		if strings.ContainsRune(row, '•') {
			landRows++
		}
	}
	if landRows < 18 {
		t.Fatalf("world silhouette too sparse: %d land rows", landRows)
	}
	compact := secAtlasMask(70)
	if len(compact) != 19 {
		t.Fatalf("compact atlas height = %d, want 19", len(compact))
	}
	if !strings.Contains(strings.Join(mask, ""), "•") {
		t.Fatal("world silhouette should preserve a dense dot field")
	}
}

func TestSecAtlasEventDoesNotInventLocation(t *testing.T) {
	event := security.Event{
		Timestamp: time.Unix(1, 0), Host: "api.example.com", Port: 443,
		Tool: "web_fetch", Verdict: security.VerdictAllowed, Classification: security.Clean,
	}
	rendered := ansi.Strip(renderSecAtlasEvent(event, true, 100))
	if !strings.Contains(rendered, "location unknown") || !strings.Contains(rendered, "api.example.com:443") {
		t.Fatalf("unexpected atlas event: %s", rendered)
	}
}

func TestSecAtlasAdvisorContextIncludesSelectedFlowWithoutPayload(t *testing.T) {
	guard := useEmptySecurityGuard(t)
	_, _ = guard.CheckOutbound("web_fetch", "api.us-east-1.example", 443, "https", "private-payload", false)
	m := model{secView: secViewState{active: true, tab: secTabAtlas}}
	context := m.secAdvisorSessionContext()
	for _, want := range []string{"egress_requests=1", "atlas_selected=", "destination=api.us-east-1.example:443", "verdict=allowed"} {
		if !strings.Contains(context, want) {
			t.Fatalf("advisor context missing %q:\n%s", want, context)
		}
	}
	if strings.Contains(context, "private-payload") {
		t.Fatal("advisor context leaked an outbound payload")
	}
}
