package tui

import (
	"encoding/base64"
	"fmt"
	"hash/fnv"
	"math"
	"os"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/aistatine/sonut/internal/security"
)

const secAtlasEventLimit = 6

type secAtlasFilter int

const (
	secAtlasFilterAll secAtlasFilter = iota
	secAtlasFilterBlocked
	secAtlasFilterSensitive
	secAtlasFilterCount
)

func (filter secAtlasFilter) String() string {
	switch filter {
	case secAtlasFilterBlocked:
		return "blocked"
	case secAtlasFilterSensitive:
		return "sensitive"
	default:
		return "all"
	}
}

type secAtlasTone int

const (
	atlasToneNone secAtlasTone = iota
	atlasToneMap
	atlasToneFaint
	atlasToneGreen
	atlasToneRed
	atlasToneAmber
	atlasToneBlue
)

type secAtlasPoint struct {
	x, y    float64
	label   string
	located bool
}

const secAtlasWorldRasterWidth = 256
const secAtlasWorldRasterHeight = 128
const secAtlasWorldRasterPacked = `
AAAAAAAAAAAAAAAAD/gAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAD//8An//AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAE//+f
/////gAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAB///P/////gAAAH4AAAAAAAPgAAAAAAAAAAAAAAAAAHB9/B/////8AAAP4AAAAA
AAAOwAAAAAAAAAAAAAAAAcAYX4f/////wAAAOYAAAAAAAAAAAAAAAAAAAAAAAAA4IB7/A/////+AAAAAAAAAAYAAAPYAAAAAAAAA
AAAAAAe4y94AA////8AAAAAAAAB4AAD//4AAPgAAAAAAAAAAQQAAYAAB////wAAAAAAAAcAAB//8AAAAAAAAAAAAAAD4DO5tgAD/
//8AAAAAAAADgAB////44AwAAAAAAAAAAe/46P/AAH///gAAAAAAAAYAdb/////wH8AAAIAAwAAAj/wcf/gAH///AAAAABAAAwH/
//////v/8AABAA//4BUH/y4P/wB///8AAAAD/AAAAPf/////////wGAAP/////3gF5w+AB//+AAAAA//4AIed///////////f+Af
/////9//vG/AP/+AAAAAP//5n//3/////////////Bf////////4D/A//gAAAAB//3j//+////////////84f////////9AfIB/w
APwAAP4/j////////////////wgH////////nE/AH+AA+AAB/n+////////////////+AR////////4AB8APwAAAAAf5////////
//////////4AP////////AHwgAfAAAAAD/H////////////////3+AA/9//////4AH4AA4AAAAAP8f///////////////i/AABfs
D/////gAfgAAAAAAAA/4D//////////////8MAAAAeAD/////AD+YAAAAAAADPB/////////////+ABwAAAAsAB////8AH/wAAAA
AA4B8H/////////////gAfAAAAMAAD////+AP/AAAAAADANh/////////////8AB8AAACAAAH/////B//AAAAAAGAoH/////////
////gAHgAAAAAAAf////+P/+AAAAAHMDn/////////////+AAcAAAAAAAA/////8//8AAAAAdx////////////////wBgAAAAAAA
B/////z//wAAAABPn///////////////9AGAAAAAAAAH///////9AAAAAAd////////////////0AAAAAAAAAAL//////yEAAAAA
AP////////////////YAAAAAAAAAAX/////+w8AAAAAH////////////////9AAAAAAAAAAA///////AIAAAAAP/////////////
///kAAAAAAAAAAD//////9AAAAAAAf///Z/j/////////8AAAAAAAAAAAP//////sAAAAAAB/7/5/8//////////gAAAAAAAAAAA
//////5AAAAAAAH9n/APz/////////8GAAAAAAAAAAH//////AAAAAAAf8HP8AfH/////////gwAAAAAAAAAAP/////8AAAAAAA/
wmPxg+f////////wAAAAAAAAAAAA//////AAAAAAAD+CO4f/4////////fAIAAAAAAAAAAD/////4AAAAAAAfwITH//j///////6
YAgAAAAAAAAAAP/////gAAAAAAB/ABGf/+P///////BgEAAAAAAAAAAAf////8AAAAAAAD4AYR//8////////jAwAAAAAAAAAAB/
////wAAAAAAAAP8AAX/////////8MHAAAAAAAAAAAD/////AAAAAAAAP/wAhP/////////gz8AAAAAAAAAAAD////4AAAAAAAB/+
AAB//////////AeAAAAAAAAAAAAH///+AAAAAAAAP//AAH/////////8DgAAAAAAAAAAAAX///wAAAAAAAB//+PAf/////////4A
AAAAAAAAAAAABP///AAAAAAAAH//+////////////AAAAAAAAAAAAAAC//kEAAAAAAAAf//////P///////+AAAAAAAAAAAAAAF/
4AYAAAAAAAD////+f+////////4AAAAAAAAAAAAAAX/gBgAAAAAAA/////9/4////////AAAAAAAAAAAAAAAv+ACAAAAAAAD////
/z/4f//////4AAAAAAAAAAAAAACf4ACAAAAAAAf/////v/kA//////oAAAAAAAAAAAAAAE/gAAAAAAAAB/////+f/4D/////8AAA
AAAAAAAAAAAAB8AOAAAAAAAP/////8//wH//f//AAAAAAAAAAQAAAAAH4ACAAAAAAA//////z//AP/x/+wAAAAAAAAAAAAAAAAfg
wGAAAAAAD//////P/4AP/D/yAAAAAAAAAABAAAAAB/HADAAAAAAP/////+f/gA/wH+YAAAAAAAAAAAAAAAAB/4DQgAAAAAf/////
4/8AD/Af8AIAAAAAAAAAAAAAAAD/gAAAAAAAD//////z/gAPwB/wAgAAAAAAAAAAAAAAABeQAAAAAAAP//////P4AA+AF/gGAAAA
AAAAAAAAAAAAA/gAAAAAAA//////+/AAB4AD/AQAAAAAAAAAAAAAAAAA+AAAAAAAD//////9wAAHgAP8AQAAAAAAAAAAAAAAAAA4
AAAAAAAP//////4AAAeAAvwAAAAAAAAAAAAAAAAAABAKAAAAAAf//////hAAA4ACeAGAAAAAAAAAAAAAAAAAGD+oAAAAA///////
8AADAAIwAQAAAAAAAAAAAAAAAAANv/gAAAAD///////wAANAAgABQAAAAAAAAAAAAAAAAAL//AAAAAH///////AAAEACAAHAAAAA
AAAAAAAAAAAAAH/8AAAAAP//////4AAAQAEAAIAAAAAAAAAAAAAAAAAAf/+AAAAAf5/////gAAAAAYAwAAAAAAAAAAAAAAAAAAB/
/+AAAAAgD////8AAAAAMwHAAAAAAAAAAAAAAAAAAAH//8AAAAAAB////wAAAAAbAcAAAAAAAAAAAAAAAAAAA///wAAAAAAH///+A
AAAABsHwAAAAAAAAAAAAAAAAAAD///AAAAAAAf///wAAAAADg/AAAAAAAAAAAAAAAAAAAf//8AAAAAAB///+AAAAAAHD8AAAAAAA
AAAAAAAAAAAB///6AAAAAAP///wAAAAAAcPnBgAAAAAAAAAAAAAAAAH///8AAAAAAf//+AAAAAAAw+QDYAAAAAAAAAAAAAAAAf//
//AAAAAB///4AAAAAADgZhv4AAAAAAAAAAAAAAAD////+AAAAAD///AAAAAAAGAGAP4AAAAAAAAAAAAAAAP////+AAAAAH//8AAA
AAAAIAQAPxAAAAAAAAAAAAAAAf////4AAAAAf//wAAAAAAAYAAE/ggAAAAAAAAAAAAAB/////gAAAAB///AAAAAAAAOAAD+AAAAA
AAAAAAAAAAD////+AAAAAH//8AAAAAAAABZADMAgAAAAAAAAAAAAAP////wAAAAAf//wAAAAAAAABIAAYAAAAAAAAAAAAAAAf///
/AAAAAA///gAAAAAAAAAAAQAAAAAAAAAAAAAAAB////4AAAAAD//+AAAAAAAAAADhAAAAAAAAAAAAAAAAD////AAAAAAf//4EAAA
AAAAAAeEAAAAAAAAAAAAAAAAP///8AAAAAB///gwAAAAAAAAbwYAAAAAAAAAAAAAAAAf///wAAAAAP//+GAAAAAAAAD/hgAAAAAA
AAAAAAAAAA////AAAAAA///w4AAAAAAAAf/PAAABAAAAAAAAAAAAB///8AAAAAD//8HgAAAAAAAB//8AAAIAAAAAAAAAAAAD///w
AAAAAH//geAAAAAAAAP//wAAAAAAAAAAAAAAAAP//+AAAAAAf/+A4AAAAAAAB///gAAAAAAAAAAAAAAAA///4AAAAAA//4HAAAAA
AAA////ABAAAAAAAAAAAAAAD///AAAAAAD//gcAAAAAAAH///+ACAAAAAAAAAAAAAAP//wAAAAAAP/+BwAAAAAAAf///4AAAAAAA
AAAAAAAAA//8AAAAAAA//4HAAAAAAAB////wAAAAAAAAAAAAAAAD//wAAAAAAB/+AAAAAAAAAH////gAAAAAAAAAAAAAAAP/+AAA
AAAAH/4AAAAAAAAAf///+AAAAAAAAAAAAAAAB//4AAAAAAAf/gAAAAAAAAB////4AAAAAAAAAAAAAAAH//gAAAAAAA/+AAAAAAAA
AD////gAAAAAAAAAAAAAAAf/8AAAAAAAD/wAAAAAAAAAP///+AAAAAAAAAAAAAAAB//wAAAAAAAH+AAAAAAAAAA////4AAAAAAAA
AAAAAAAH/+AAAAAAAAfwAAAAAAAAAD+B//AAAAAAAAAAAAAAAAf/wAAAAAAAB+AAAAAAAAAAPwD/8AAAAAAAAAAAAAAAB/6AAAAA
AAAAAAAAAAAAAAAQAL/gAAAAAAAAAAAAAAAP/gAAAAAAAAAAAAAAAAAAAAAAH+AAEAAAAAAAAAAAAA//AAAAAAAAAAAAAAAAAAAA
AAAf4AAIAAAAAAAAAAAAD/4AAAAAAAAAAAAAAAAAAAAAAA+AAA4AAAAAAAAAAAAP8AAAAAAAAAAAAAAAAAAAAAAAAAAADAAAAAAA
AAAAAA/wAAAAAAAAAAAAAAAAAAAAAAAAAAAEAAAAAAAAAAAAH8AAAAAAAAAAAAAAAAAAAAAAAAGAADgAAAAAAAAAAAAf4AAAAAAA
AAAAAAAAAAAAAAAAAYAAIAAAAAAAAAAAAA/AAAAAAAAAAAAAAAAAAAAAAAAAAADgAAAAAAAAAAAAH4AAAAAAAAAAAAAAAAAAAAAA
AAAAAcAAAAAAAAAAAAAfAAAAAAAAAAAAAAAAAAAAAAAAAAADgAAAAAAAAAAAAB+AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA
AAAAP4AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/AAAAAAAAAAAAAAAAQAAAAAAAAAAAAAAAAAAAAAAAAD4AAAAAAAAA
AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAHgYAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAeAAAAAAAAAAAAAAAAAAAAAAAA
AAAAAAAAAAAAAAAAAA8AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAB8AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA
AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA
AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA==
`

// A fixed, high-resolution terminal mask keeps the coastlines stable across
// window sizes. It is deliberately text-only: no browser, image asset, or
// runtime map service is needed by /sec.
var secAtlasWorldRaster = decodeSecAtlasWorldRaster()

func decodeSecAtlasWorldRaster() [][]bool {
	data, err := base64.StdEncoding.DecodeString(secAtlasWorldRasterPacked)
	if err != nil {
		panic("invalid security atlas world raster")
	}
	land := make([][]bool, secAtlasWorldRasterHeight)
	for y := range land {
		land[y] = make([]bool, secAtlasWorldRasterWidth)
		for x := range land[y] {
			index := y*secAtlasWorldRasterWidth + x
			land[y][x] = data[index/8]&(1<<uint(7-index%8)) != 0
		}
	}
	return land
}

func secAtlasBuildWorldMask(width, height int) []string {
	land := make([][]bool, height)
	for y := range land {
		land[y] = make([]bool, width)
		sy := minInt(secAtlasWorldRasterHeight-1, int(float64(y)*float64(secAtlasWorldRasterHeight)/float64(height)))
		for x := 0; x < width; x++ {
			sx := minInt(secAtlasWorldRasterWidth-1, int(float64(x)*float64(secAtlasWorldRasterWidth)/float64(width)))
			land[y][x] = secAtlasWorldRaster[sy][sx]
		}
	}
	result := make([]string, height)
	for y := range land {
		var row strings.Builder
		row.Grow(width)
		for x := range land[y] {
			if !land[y][x] {
				row.WriteByte(' ')
			} else {
				row.WriteRune('•')
			}
		}
		result[y] = row.String()
	}
	return result
}

func secAtlasMask(width int) []string {
	mapWidth := minInt(112, maxInt(28, width))
	height := 27
	if width < 76 {
		height = 19
	}
	return secAtlasBuildWorldMask(mapWidth, height)
}

func secAtlasResizeMask(source []string, width, height int) []string {
	if len(source) == 0 || width <= 0 || height <= 0 {
		return nil
	}
	sourceWidth := 0
	for _, row := range source {
		sourceWidth = maxInt(sourceWidth, len([]rune(row)))
	}
	land := make([][]bool, height)
	for y := range land {
		land[y] = make([]bool, width)
		sy := minInt(len(source)-1, int(float64(y)*float64(len(source))/float64(height)))
		sourceRow := []rune(source[sy])
		for x := 0; x < width; x++ {
			sx := minInt(sourceWidth-1, int(float64(x)*float64(sourceWidth)/float64(width)))
			land[y][x] = sx < len(sourceRow) && sourceRow[sx] != ' '
		}
	}
	result := make([]string, height)
	for y := range result {
		var row strings.Builder
		row.Grow(width)
		for x := 0; x < width; x++ {
			if !land[y][x] {
				row.WriteByte(' ')
			} else {
				row.WriteRune('•')
			}
		}
		result[y] = row.String()
	}
	return result
}

func (m model) renderSecAtlas(width int) []string {
	guard := security.Default()
	allEvents := guard.Events()
	events := secAtlasFilteredEvents(allEvents, m.secView.atlasFilter)
	selected := 0
	if len(events) > 0 {
		selected = clampInt(m.secView.atlasSelected, 0, len(events)-1)
	}

	status := sonutTheme.green.Render("● LIVE")
	if m.secView.atlasPaused || m.reducedMotion {
		status = sonutTheme.faint.Render("● STEADY")
	}
	lines := []string{
		"",
		secSection("Security atlas", width, status),
		sonutTheme.faint.Render("Observed /sec egress decisions · agentless interface inventory · packet capture off"),
		"",
	}
	lines = append(lines, renderSecAtlasMap(events, width, m.secView.atlasFrame, selected)...)
	lines = append(lines,
		"",
		secAtlasLegend(m.secView.atlasFilter),
		sonutTheme.faint.Render("The green local dot follows the best local region evidence; unknown network locations stay on the unlocated rail."),
		sonutTheme.faint.Render("Routes show observed destinations. Region placement is used only when the hostname contains a known cloud region; ? endpoints are deliberately unlocated."),
		"",
		secSection("Flow decisions", width, pluralCount(len(events), "visible")),
	)
	if len(events) == 0 {
		lines = append(lines,
			sonutTheme.green.Render("● Quiet session"),
			sonutTheme.muted.Render("  No event matches this filter. /sec will animate real guarded requests as they occur."),
		)
	} else {
		visible := events
		if len(visible) > secAtlasEventLimit {
			visible = visible[:secAtlasEventLimit]
		}
		for index, event := range visible {
			lines = append(lines, renderSecAtlasEvent(event, index == selected, width))
			if index == selected && m.secView.atlasExpanded {
				lines = append(lines, renderSecAtlasEventDetail(event, width)...)
			}
		}
	}

	lines = append(lines, "", secSection("Local networks", width, "passive inventory"))
	if len(m.secView.atlasInterfaces) == 0 {
		lines = append(lines, sonutTheme.faint.Render("No active non-loopback IPv4 interface was visible."))
	} else {
		for index, iface := range m.secView.atlasInterfaces {
			if index == 4 {
				lines = append(lines, sonutTheme.faint.Render(fmt.Sprintf("+ %d additional local address(es)", len(m.secView.atlasInterfaces)-index)))
				break
			}
			badge := sonutTheme.blue.Render(strings.ToUpper(iface.Kind))
			lines = append(lines, badge+"  "+sonutTheme.ink.Bold(true).Render(iface.Name)+"  "+sonutTheme.muted.Render(iface.CIDR)+"  "+sonutTheme.faint.Render(iface.Label))
		}
	}
	lines = append(lines, sonutTheme.faint.Render("No LAN probes, DNS lookups, online GeoIP calls, or privileged sniffing are performed by this view."))
	return lines
}

func secAtlasFilteredEvents(events []security.Event, filter secAtlasFilter) []security.Event {
	filtered := make([]security.Event, 0, len(events))
	for _, event := range events {
		switch filter {
		case secAtlasFilterBlocked:
			if event.Verdict != security.VerdictBlocked {
				continue
			}
		case secAtlasFilterSensitive:
			if event.Classification != security.Sensitive && event.Classification != security.Blocked {
				continue
			}
		}
		filtered = append(filtered, event)
	}
	return filtered
}

func renderSecAtlasMap(events []security.Event, width, frame, selected int) []string {
	mask := secAtlasMask(width)
	height := len(mask)
	mapWidth := 0
	for _, row := range mask {
		mapWidth = maxInt(mapWidth, len([]rune(row)))
	}
	cells := make([][]rune, height)
	tones := make([][]secAtlasTone, height)
	for y, row := range mask {
		cells[y] = make([]rune, mapWidth)
		tones[y] = make([]secAtlasTone, mapWidth)
		for x := 0; x < mapWidth; x++ {
			cells[y][x] = ' '
		}
		for x, char := range []rune(row) {
			if char != ' ' {
				cells[y][x] = char
				tones[y][x] = atlasToneMap
			}
		}
	}

	visible := events
	if len(visible) > secAtlasEventLimit {
		visible = visible[:secAtlasEventLimit]
	}
	hasUnlocated := false
	for _, event := range visible {
		if !secAtlasPointForHost(event.Host).located {
			hasUnlocated = true
			break
		}
	}
	trailY := maxInt(0, height-1)
	localPoint := secAtlasLocalPoint()
	originX, originY := mapWidth/2, trailY
	if localPoint.located {
		originX = clampInt(int(localPoint.x*float64(mapWidth-1)), 0, mapWidth-1)
		originY = clampInt(int(localPoint.y*float64(maxInt(1, height-2))), 0, maxInt(0, height-2))
	}
	if originY >= 0 {
		// Keep a visible local anchor even during a quiet session. This is a
		// regional statement from local timezone evidence, not a guessed city
		// or public-IP geolocation.
		cells[originY][originX] = '●'
		tones[originY][originX] = atlasToneGreen
	}
	if hasUnlocated {
		for x := maxInt(0, mapWidth/10); x < mapWidth-mapWidth/10; x++ {
			if cells[trailY][x] == ' ' {
				cells[trailY][x] = '·'
				tones[trailY][x] = atlasToneFaint
			}
		}
	}
	for index, event := range visible {
		point := secAtlasPointForHost(event.Host)
		destinationX := clampInt(int(point.x*float64(mapWidth-1)), 0, mapWidth-1)
		destinationY := maxInt(0, height-2)
		if !point.located {
			destinationY = trailY
		} else {
			destinationY = clampInt(int(point.y*float64(maxInt(1, height-2))), 0, maxInt(0, height-2))
		}
		tone := secAtlasToneForEvent(event)
		steps := maxInt(14, secAtlasAbsInt(destinationX-originX)+4)
		for step := 1; step < steps; step++ {
			t := float64(step) / float64(steps)
			x := int(math.Round(float64(originX) + float64(destinationX-originX)*t))
			y := int(math.Round(float64(originY) + float64(destinationY-originY)*t - math.Sin(math.Pi*t)*float64(maxInt(2, height/5))))
			if x < 0 || x >= mapWidth || y < 0 || y >= height || (step+frame+index)%3 != 0 {
				continue
			}
			cells[y][x] = '·'
			tones[y][x] = atlasToneFaint
		}
		marker := '•'
		if !point.located {
			marker = '?'
		}
		if event.Verdict == security.VerdictBlocked {
			marker = '×'
		}
		if index == selected {
			marker = '◆'
		}
		cells[destinationY][destinationX] = marker
		tones[destinationY][destinationX] = tone
	}

	lines := make([]string, 0, height)
	for y := range cells {
		var line strings.Builder
		start := 0
		for start < mapWidth {
			tone := tones[y][start]
			end := start + 1
			for end < mapWidth && tones[y][end] == tone {
				end++
			}
			line.WriteString(secAtlasToneStyle(tone).Render(string(cells[y][start:end])))
			start = end
		}
		lines = append(lines, line.String())
	}
	return lines
}

func secAtlasLocalPoint() secAtlasPoint {
	zone, offset := time.Now().Zone()
	return secAtlasLocalPointForZone(strings.TrimSpace(os.Getenv("TZ")), zone, offset)
}

func secAtlasLocalPointForZone(timezone, zone string, offset int) secAtlasPoint {
	region := strings.ToLower(strings.TrimSpace(timezone + " " + zone))
	// The local interface only exposes private address space. Use the local
	// timezone as a transparent regional hint, never as city-level precision.
	if strings.Contains(region, "asia/kolkata") || strings.Contains(region, "asia/calcutta") ||
		(strings.EqualFold(strings.TrimSpace(zone), "IST") && offset == 5*60*60+30*60) {
		return secAtlasPoint{0.72, 0.46, "India · local timezone", true}
	}
	return secAtlasPoint{x: 0.5, y: 0.96, label: "unlocated", located: false}
}

func secAtlasPointForHost(host string) secAtlasPoint {
	lower := strings.ToLower(strings.TrimSpace(host))
	regions := []struct {
		needles []string
		point   secAtlasPoint
	}{
		{[]string{"us-west", "westus"}, secAtlasPoint{0.17, 0.34, "North America west", true}},
		{[]string{"us-east", "eastus"}, secAtlasPoint{0.27, 0.34, "North America east", true}},
		{[]string{"sa-east", "brazilsouth"}, secAtlasPoint{0.37, 0.70, "South America", true}},
		{[]string{"eu-west", "westeurope", "uksouth", "ukwest"}, secAtlasPoint{0.50, 0.25, "Europe west", true}},
		{[]string{"eu-central", "centraleurope", "germanywestcentral", "northeurope", "swedencentral"}, secAtlasPoint{0.55, 0.26, "Europe central", true}},
		{[]string{"af-south", "southafricanorth"}, secAtlasPoint{0.56, 0.79, "Africa south", true}},
		{[]string{"ap-southeast", "southeastasia", "singapore"}, secAtlasPoint{0.79, 0.58, "Southeast Asia", true}},
		{[]string{"ap-south-", "centralindia", "southindia"}, secAtlasPoint{0.72, 0.46, "India", true}},
		{[]string{"ap-northeast", "japaneast", "japanwest", "tokyo"}, secAtlasPoint{0.84, 0.34, "Japan", true}},
		{[]string{"australia", "sydney"}, secAtlasPoint{0.92, 0.81, "Australia", true}},
	}
	for _, region := range regions {
		for _, needle := range region.needles {
			if strings.Contains(lower, needle) {
				return region.point
			}
		}
	}
	// Never turn a hash into fake geography. Unknown destinations live on a
	// dedicated lower rail and retain an explicit ? marker.
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(lower))
	x := 0.12 + float64(hash.Sum32()%76)/100
	return secAtlasPoint{x: x, y: 0.96, label: "unlocated", located: false}
}

func secAtlasToneForEvent(event security.Event) secAtlasTone {
	switch {
	case event.Verdict == security.VerdictBlocked:
		return atlasToneRed
	case event.Classification == security.Sensitive:
		return atlasToneAmber
	case event.Verdict == security.VerdictAllowedOnce:
		return atlasToneBlue
	default:
		return atlasToneGreen
	}
}

func secAtlasToneStyle(tone secAtlasTone) lipgloss.Style {
	switch tone {
	case atlasToneMap:
		return sonutTheme.blue
	case atlasToneFaint:
		return sonutTheme.faintest
	case atlasToneGreen:
		return sonutTheme.green.Bold(true)
	case atlasToneRed:
		return sonutTheme.red.Bold(true)
	case atlasToneAmber:
		return sonutTheme.amber.Bold(true)
	case atlasToneBlue:
		return sonutTheme.blue.Bold(true)
	default:
		return sonutTheme.ink
	}
}

func secAtlasLegend(filter secAtlasFilter) string {
	return sonutTheme.green.Bold(true).Render("● LOCAL ACTIVE") + "   " +
		sonutTheme.green.Bold(true).Render("● ALLOWED") + "   " +
		sonutTheme.amber.Bold(true).Render("▲ SENSITIVE") + "   " +
		sonutTheme.red.Bold(true).Render("× BLOCKED") + "   " +
		sonutTheme.faint.Render("? UNLOCATED") + "   " +
		sonutTheme.ink.Render("FILTER "+strings.ToUpper(filter.String()))
}

func renderSecAtlasEvent(event security.Event, selected bool, width int) string {
	marker, status, style := "●", "ALLOWED", sonutTheme.green
	switch {
	case event.Verdict == security.VerdictBlocked:
		marker, status, style = "×", "BLOCKED", sonutTheme.red
	case event.Classification == security.Sensitive:
		marker, status, style = "▲", "SENSITIVE", sonutTheme.amber
	case event.Verdict == security.VerdictAllowedOnce:
		marker, status, style = "●", "ALLOWED ONCE", sonutTheme.blue
	}
	destination := event.Host
	if event.Port != 0 {
		destination += fmt.Sprintf(":%d", event.Port)
	}
	point := secAtlasPointForHost(event.Host)
	location := point.label
	if !point.located {
		location = "location unknown"
	}
	prefix := "  "
	if selected {
		prefix = "› "
	}
	line := prefix + style.Bold(true).Render(marker+" "+status) + "  " + sonutTheme.ink.Bold(true).Render(event.Tool) + "  " + sonutTheme.blue.Render(ansi.Truncate(destination, maxInt(12, width/3), "…")) + "  " + sonutTheme.faint.Render(location+" · "+event.Timestamp.Local().Format("15:04:05"))
	if selected {
		return fillPaletteLine(line, width, func(style lipgloss.Style) lipgloss.Style { return sonutTheme.onSel(style) })
	}
	return line
}

func renderSecAtlasEventDetail(event security.Event, width int) []string {
	protocol := strings.ToUpper(strings.TrimSpace(event.Protocol))
	if protocol == "" {
		protocol = "unspecified protocol"
	}
	evidence := "direct"
	if event.BestEffort {
		evidence = "best-effort"
	}
	meta := fmt.Sprintf("%s · %s observed · %s evidence", protocol, formatSecBytes(int64(event.Bytes)), evidence)
	lines := []string{"    " + sonutTheme.blue.Render(meta)}
	if reason := strings.TrimSpace(event.Reason); reason != "" {
		for _, line := range wrapPlainText(reason, maxInt(18, width-8)) {
			lines = append(lines, "    "+secAtlasToneStyle(secAtlasToneForEvent(event)).Render(line))
		}
	}
	lines = append(lines, "    "+sonutTheme.faint.Render("No request payload or secret value is displayed."))
	return lines
}

func secAtlasAbsInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
