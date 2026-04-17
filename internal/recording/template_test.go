package recording

import (
	"strings"
	"testing"
)

func TestShellSplit(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"ffmpeg -i input.mp4", []string{"ffmpeg", "-i", "input.mp4"}},
		{`ffmpeg -i "hello world" out.mp4`, []string{"ffmpeg", "-i", "hello world", "out.mp4"}},
		{`ffmpeg -metadata comment="has \"quotes\" inside" out.mp4`, []string{"ffmpeg", "-metadata", `comment=has "quotes" inside`, "out.mp4"}},
		{`ffmpeg -i 'single quoted with spaces' out.mp4`, []string{"ffmpeg", "-i", "single quoted with spaces", "out.mp4"}},
		{"one   two\tthree\n  four", []string{"one", "two", "three", "four"}},
		{"", nil},
	}
	for _, tt := range tests {
		got, err := shellSplit(tt.in)
		if err != nil {
			t.Errorf("shellSplit(%q) err=%v", tt.in, err)
			continue
		}
		if len(got) != len(tt.want) {
			t.Errorf("shellSplit(%q) = %v, want %v", tt.in, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("shellSplit(%q)[%d] = %q, want %q", tt.in, i, got[i], tt.want[i])
			}
		}
	}
}

func TestShellSplit_UnterminatedQuoteErrors(t *testing.T) {
	if _, err := shellSplit(`ffmpeg -i "still open`); err == nil {
		t.Errorf("expected unterminated-quote error")
	}
}

// TestParseRecordTemplate_RejectsUnknownTokens pins the core contract:
// unknown tokens fail parsing and come back listed so the issues-
// registry entry can show every mistake at once.
func TestParseRecordTemplate_RejectsUnknownTokens(t *testing.T) {
	_, err := ParseRecordTemplate("ffmpeg -i {rtsp_url} -tag {typo} {also_wrong} {output}")
	if err == nil {
		t.Fatal("expected error on unknown tokens")
	}
	msg := err.Error()
	for _, want := range []string{"{typo}", "{also_wrong}"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error missing %q: %s", want, msg)
		}
	}
}

func TestParseRecordTemplate_EmptyErrors(t *testing.T) {
	if _, err := ParseRecordTemplate(""); err == nil {
		t.Errorf("expected error on empty template")
	}
}

// TestRecordTemplate_Expand exercises every supported token so future
// additions to recordTokens have a visible-in-tests expectation.
func TestRecordTemplate_Expand(t *testing.T) {
	tmpl, err := ParseRecordTemplate(
		`ffmpeg -hide_banner -rtsp_transport tcp -i {rtsp_url} ` +
			`-c:v libx264 -preset ultrafast -f segment -segment_time {segment_sec} ` +
			`-strftime 1 -metadata service_name="{cam_name}" ` +
			`{output_stem}.webm`,
	)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := tmpl.Expand(TemplateContext{
		CamName:    "front_door",
		Quality:    "hd",
		RtspHost:   "127.0.0.1",
		RtspPort:   8554,
		Output:     "/media/recordings/front_door/2026/04/20/12-34-56.mp4",
		SegmentSec: 45,
	})
	joined := strings.Join(got, " ")
	for _, want := range []string{
		"-rtsp_transport tcp",
		"-i rtsp://127.0.0.1:8554/front_door",
		"-segment_time 45",
		`service_name=front_door`, // quoted in the template, argv-split keeps the content
		"/media/recordings/front_door/2026/04/20/12-34-56.webm",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in expanded argv:\n%s", want, joined)
		}
	}
}

// TestRecordTemplate_Expand_CAMUppercase covers the {CAM_NAME} variant.
func TestRecordTemplate_Expand_CAMUppercase(t *testing.T) {
	tmpl, _ := ParseRecordTemplate("cp {output} /mnt/archive/{CAM_NAME}/")
	got := tmpl.Expand(TemplateContext{
		CamName: "shed_camera",
		Output:  "/tmp/x.mp4",
	})
	if got[2] != "/mnt/archive/SHED_CAMERA/" {
		t.Errorf("got[2]=%q, want /mnt/archive/SHED_CAMERA/", got[2])
	}
}

// TestRecordTemplate_LeavesLiteralBracesAlone verifies our substitute
// pass doesn't eat user content that happens to contain braces (e.g.
// ffmpeg filter expressions like "[0:v]{drawtext...}").
func TestRecordTemplate_LeavesLiteralBracesAlone(t *testing.T) {
	tmpl, err := ParseRecordTemplate(`ffmpeg -i {rtsp_url} -vf {some filter text} {output}`)
	if err != nil {
		// "{some filter text}" fails isValidTokenName (has spaces) so
		// it passes through unchanged at parse time — not counted as
		// an unknown token. If the user meant a literal filter spec,
		// it survives.
		t.Fatalf("parse should succeed (space in braces → literal): %v", err)
	}
	got := tmpl.Expand(TemplateContext{
		CamName: "c", RtspHost: "h", RtspPort: 1, Output: "/o.mp4",
	})
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "{some filter text}") {
		t.Errorf("literal braces stripped: %s", joined)
	}
}
