// attachment_view.go is the Grok-style drill-in for a staged image/document.
// Opened from the composer chips (click / /image view / enter on chip focus),
// it shows Format / Dimensions / Size / Path so the user can inspect or drop
// an attachment before sending — matching the reference attachment card.
package tui

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/aistatine/sonut/internal/sonutruntime"
)

// attachmentViewState manages the inspector overlay for staged attachments.
// When active, the chat body is replaced by a metadata card (and optional
// open-in-system action). Esc returns to chat.
type attachmentViewState struct {
	active bool
	// index into the combined list: images first, then documents.
	index              int
	parentScrollOffset int
}

// openAttachmentView activates the inspector on the given combined index
// (0-based across images then docs). Clamps to available attachments.
func (m model) openAttachmentView(index int) model {
	total := len(m.pendingImageLabels) + len(m.pendingDocuments)
	if total == 0 {
		return m
	}
	if index < 0 {
		index = 0
	}
	if index >= total {
		index = total - 1
	}
	if !m.attachmentView.active {
		m.attachmentView.parentScrollOffset = m.chatScrollOffset
	}
	m.attachmentView.active = true
	m.attachmentView.index = index
	m.chatScrollOffset = 0
	m = m.clearHover()
	return m
}

// exitAttachmentView deactivates the inspector and restores chat scroll.
func (m model) exitAttachmentView() model {
	if !m.attachmentView.active {
		return m
	}
	m.chatScrollOffset = m.attachmentView.parentScrollOffset
	m.attachmentView = attachmentViewState{}
	m = m.clearHover()
	return m
}

// attachmentViewNavBar is the header while the inspector is open.
func (m model) attachmentViewNavBar(width int) string {
	total := len(m.pendingImageLabels) + len(m.pendingDocuments)
	label := m.attachmentViewLabel()
	left := sonutTheme.accent.Render("← attachment") +
		sonutTheme.faint.Render(fmt.Sprintf("  ·  %d/%d  ·  %s", m.attachmentView.index+1, total, truncateMiddle(label, maxInt(8, width/3))))
	right := sonutTheme.faint.Render("← → cycle · x remove · o open · esc back")
	return fitStyledLine(joinHeaderLine(left, right, width), width)
}

func (m model) attachmentViewLabel() string {
	nImg := len(m.pendingImageLabels)
	i := m.attachmentView.index
	if i < nImg {
		return m.pendingImageLabels[i]
	}
	di := i - nImg
	if di >= 0 && di < len(m.pendingDocuments) {
		return m.pendingDocuments[di].label
	}
	return "attachment"
}

// attachmentViewBodyItems builds the metadata card for the active attachment.
func (m model) attachmentViewBodyItems(width int) []transcriptBodyItem {
	card := m.renderAttachmentCard(width)
	return []transcriptBodyItem{transcriptBlockBodyItem(transcriptBodyItemRow, -1, card)}
}

// renderAttachmentCard builds a Grok-style metadata block:
//
//	Format: PNG
//	Dimensions: 904 × 868
//	Size: 63.9 KB
//	Path: /Users/…/photo.png
func (m model) renderAttachmentCard(width int) string {
	inner := maxInt(20, width-4)
	nImg := len(m.pendingImageLabels)
	i := m.attachmentView.index

	var lines []string
	if i < nImg {
		label := m.pendingImageLabels[i]
		var block sonutruntime.ImageBlock
		if i < len(m.pendingImages) {
			block = m.pendingImages[i]
		}
		path := ""
		if i < len(m.pendingImagePaths) {
			path = m.pendingImagePaths[i]
		}
		format := mediaTypeToFormat(block.MediaType)
		if format == "" {
			format = strings.ToUpper(strings.TrimPrefix(filepath.Ext(label), "."))
			if format == "" {
				format = "image"
			}
		}
		w, h := imageDimensions(block.Data)
		size := humanBytes(int64(len(block.Data)))
		title := fmt.Sprintf("Image %d", i+1)
		lines = append(lines, sonutTheme.accent.Render(title)+"  "+sonutTheme.faint.Render(label))
		lines = append(lines, "")
		lines = append(lines, kvLine("Format", format, inner))
		if w > 0 && h > 0 {
			lines = append(lines, kvLine("Dimensions", fmt.Sprintf("%d × %d", w, h), inner))
		} else {
			lines = append(lines, kvLine("Dimensions", "—", inner))
		}
		lines = append(lines, kvLine("Size", size, inner))
		if path != "" {
			lines = append(lines, kvLine("Path", path, inner))
		} else if label == "clipboard" {
			lines = append(lines, kvLine("Path", "(from clipboard)", inner))
		} else {
			lines = append(lines, kvLine("Path", label, inner))
		}
	} else {
		di := i - nImg
		if di < 0 || di >= len(m.pendingDocuments) {
			return sonutTheme.faint.Render("No attachment selected.")
		}
		doc := m.pendingDocuments[di]
		title := fmt.Sprintf("Document %d", di+1)
		lines = append(lines, sonutTheme.accent.Render(title)+"  "+sonutTheme.faint.Render(doc.label))
		lines = append(lines, "")
		lines = append(lines, kvLine("Format", "PDF", inner))
		lines = append(lines, kvLine("Size", humanBytes(int64(len(doc.text)))+" text", inner))
		lines = append(lines, kvLine("Path", doc.label, inner))
		preview := strings.TrimSpace(doc.text)
		if preview != "" {
			if len(preview) > 400 {
				preview = preview[:400] + "…"
			}
			lines = append(lines, "")
			lines = append(lines, sonutTheme.faint.Render("Preview:"))
			for _, pl := range strings.Split(preview, "\n") {
				lines = append(lines, sonutTheme.muted.Render(fitPlain(pl, inner)))
			}
		}
	}

	// Box the card.
	var b strings.Builder
	bar := strings.Repeat("─", maxInt(8, inner))
	b.WriteString(sonutTheme.lineStrong.Render("╭"+bar+"╮") + "\n")
	for _, line := range lines {
		fitted := fitStyledLine(line, inner)
		pad := strings.Repeat(" ", maxInt(0, inner-lipgloss.Width(fitted)))
		b.WriteString(sonutTheme.lineStrong.Render("│ ") + fitted + pad + sonutTheme.lineStrong.Render(" │") + "\n")
	}
	b.WriteString(sonutTheme.lineStrong.Render("╰" + bar + "╯"))
	return b.String()
}

func kvLine(key, value string, width int) string {
	k := sonutTheme.faint.Render(key + ":")
	// Leave room for key + space.
	rest := maxInt(8, width-lipgloss.Width(k)-1)
	return k + " " + sonutTheme.ink.Render(truncateMiddle(value, rest))
}

func mediaTypeToFormat(mt string) string {
	switch strings.ToLower(strings.TrimSpace(mt)) {
	case "image/png":
		return "PNG"
	case "image/jpeg", "image/jpg":
		return "JPEG"
	case "image/gif":
		return "GIF"
	case "image/webp":
		return "WEBP"
	default:
		if mt == "" {
			return ""
		}
		if i := strings.LastIndex(mt, "/"); i >= 0 {
			return strings.ToUpper(mt[i+1:])
		}
		return strings.ToUpper(mt)
	}
}

func imageDimensions(data []byte) (w, h int) {
	if len(data) == 0 {
		return 0, 0
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

func humanBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	if n < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
}

func truncateMiddle(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 3 {
		return string(r[:max])
	}
	// keep head…tail
	keep := max - 1
	head := keep / 2
	tail := keep - head
	return string(r[:head]) + "…" + string(r[len(r)-tail:])
}

func fitPlain(s string, width int) string {
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	if width <= 1 {
		return string(r[:width])
	}
	return string(r[:width-1]) + "…"
}

// cycleAttachmentView moves ±1 through staged attachments.
func (m model) cycleAttachmentView(delta int) model {
	total := len(m.pendingImageLabels) + len(m.pendingDocuments)
	if total == 0 || !m.attachmentView.active {
		return m
	}
	m.attachmentView.index = (m.attachmentView.index + delta%total + total) % total
	return m
}

// removeActiveAttachment drops the inspected attachment and adjusts index.
func (m model) removeActiveAttachment() model {
	if !m.attachmentView.active {
		return m
	}
	nImg := len(m.pendingImageLabels)
	i := m.attachmentView.index
	if i < nImg {
		// Remove image at i — keep paths/labels/blocks in lockstep.
		m.pendingImageLabels = append(m.pendingImageLabels[:i], m.pendingImageLabels[i+1:]...)
		if i < len(m.pendingImages) {
			m.pendingImages = append(m.pendingImages[:i], m.pendingImages[i+1:]...)
		}
		if i < len(m.pendingImagePaths) {
			m.pendingImagePaths = append(m.pendingImagePaths[:i], m.pendingImagePaths[i+1:]...)
		}
	} else {
		di := i - nImg
		if di >= 0 && di < len(m.pendingDocuments) {
			m.pendingDocuments = append(m.pendingDocuments[:di], m.pendingDocuments[di+1:]...)
		}
	}
	total := len(m.pendingImageLabels) + len(m.pendingDocuments)
	if total == 0 {
		return m.exitAttachmentView()
	}
	if m.attachmentView.index >= total {
		m.attachmentView.index = total - 1
	}
	return m
}

// openActiveAttachmentInSystem opens the source file with the OS default app
// (macOS `open`, Linux `xdg-open`, Windows `start`). Clipboard attachments
// with no path are written to a temp file first.
func (m model) openActiveAttachmentInSystem() model {
	path := m.activeAttachmentPath()
	if path == "" {
		// Materialize clipboard image to temp for viewing.
		nImg := len(m.pendingImageLabels)
		i := m.attachmentView.index
		if i >= 0 && i < nImg && i < len(m.pendingImages) {
			block := m.pendingImages[i]
			ext := ".png"
			switch block.MediaType {
			case "image/jpeg":
				ext = ".jpg"
			case "image/gif":
				ext = ".gif"
			case "image/webp":
				ext = ".webp"
			}
			tmp, err := os.CreateTemp("", "sonut-attach-*"+ext)
			if err != nil {
				return m
			}
			_, _ = tmp.Write(block.Data)
			_ = tmp.Close()
			path = tmp.Name()
		} else {
			return m
		}
	}
	_ = openPathInSystem(path)
	return m
}

func (m model) activeAttachmentPath() string {
	nImg := len(m.pendingImageLabels)
	i := m.attachmentView.index
	if i < nImg && i < len(m.pendingImagePaths) {
		return m.pendingImagePaths[i]
	}
	return ""
}

func openPathInSystem(path string) error {
	if path == "" {
		return fmt.Errorf("empty path")
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	return cmd.Start()
}
