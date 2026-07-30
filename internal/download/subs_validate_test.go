package download

import (
	"os"
	"path/filepath"
	"testing"
)

// writeASS writes body to a temp .ass and returns its path.
func writeASS(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sub.ass")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write ass: %s", err)
	}
	return path
}

const assHeader = `[Script Info]
ScriptType: v4.00+

[V4+ Styles]
Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding
Style: Default,Arial,48,&H00FFFFFF,&H000000FF,&H00000000,&HCC000000,0,0,0,0,100,100,0,0,1,2,1,2,40,40,60,1

[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
`

func TestAssHasUsableDialogue(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "plain dialogue",
			body: assHeader + "Dialogue: 0,0:00:01.00,0:00:03.00,Default,,0,0,0,,Hello world\n",
			want: true,
		},
		{
			name: "dialogue with overrides and line breaks keeps text",
			body: assHeader + `Dialogue: 0,0:00:01.00,0:00:03.00,Default,,0,0,0,,{\fad(300,200)\pos(960,980)}Olá, mundo!\NSecond line
`,
			want: true,
		},
		{
			name: "no events section",
			body: "[Script Info]\nScriptType: v4.00+\n",
			want: false,
		},
		{
			name: "events header but no dialogue lines",
			body: assHeader,
			want: false,
		},
		{
			name: "dialogue line with only override blocks, no plain text",
			body: assHeader + `Dialogue: 0,0:00:01.00,0:00:03.00,Default,,0,0,0,,{\an2\fad(200,200)}
`,
			want: false,
		},
		{
			name: "comment line is not dialogue",
			body: assHeader + "Comment: 0,0:00:01.00,0:00:03.00,Default,,0,0,0,,A comment\n",
			want: false,
		},
		{
			name: "text containing a comma stays whole",
			body: assHeader + "Dialogue: 0,0:00:01.00,0:00:03.00,Default,,0,0,0,,Hello, world\n",
			want: true,
		},
		{
			// A signs-only line: drawing mode on for the whole line, no dialogue.
			// mov_text can't render drawings, so this is not renderable and must be
			// skipped (otherwise it muxes as literal "m 0 0 l ..." garbage text).
			name: "drawing-only signs line is skipped",
			body: assHeader + `Dialogue: 0,0:00:01.00,0:00:03.00,Signs,,0,0,0,,{\p1}m 0 0 l 100 0 100 100 0 100{\p0}
`,
			want: false,
		},
		{
			// Drawing then real text after \p0: the dialogue part counts.
			name: "drawing block followed by dialogue keeps the dialogue",
			body: assHeader + `Dialogue: 0,0:00:01.00,0:00:03.00,Default,,0,0,0,,{\p1}m 0 0 l 50{\p0}A sign label
`,
			want: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := writeASS(t, tc.body)
			got, err := assHasUsableDialogue(path)
			if err != nil {
				t.Fatalf("assHasUsableDialogue: %s", err)
			}
			if got != tc.want {
				t.Errorf("assHasUsableDialogue = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAssHasUsableDialogueMissingFile(t *testing.T) {
	if _, err := assHasUsableDialogue(filepath.Join(t.TempDir(), "nope.ass")); err == nil {
		t.Error("expected error for missing file, got nil")
	}
}