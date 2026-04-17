package recording

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Supported tokens for RECORD_CMD / RECORD_CMD_<CAM> templates.
//
// Token substitution happens at expand time per camera. Unknown
// tokens are rejected at parse time (parseRecordTemplate returns an
// error) so bad templates fail loudly with an issue on /metrics,
// not silently by spawning a misconfigured ffmpeg.
//
// Add new tokens here when expanding the surface — tests under
// template_test.go pin the list.
var recordTokens = map[string]struct{}{
	"cam_name":    {},
	"CAM_NAME":    {},
	"rtsp_url":    {},
	"rtsp_host":   {},
	"rtsp_port":   {},
	"output":      {},
	"output_dir":  {},
	"output_stem": {},
	"segment_sec": {},
	"quality":     {},
}

// TemplateContext holds the values that fill in the {…} tokens at
// expand time. Populated by the recorder once per spawn.
type TemplateContext struct {
	CamName    string
	Quality    string
	RtspHost   string
	RtspPort   int
	Output     string // full path ending in .mp4 (or whatever extension the caller wants)
	OutputDir  string // filepath.Dir(Output)
	OutputStem string // Output minus the final extension
	SegmentSec int
}

// RecordTemplate is a parsed, validated RECORD_CMD override. Construct
// with ParseRecordTemplate; expand via Expand.
type RecordTemplate struct {
	raw  string
	argv []string // tokens; {name} placeholders still present
}

// ParseRecordTemplate tokenizes the template into argv (shell-style
// quoting respected) and validates every {…} placeholder against the
// known token set. Returns all unknown tokens found so the caller can
// surface the complete list in a single issue.
func ParseRecordTemplate(raw string) (*RecordTemplate, error) {
	argv, err := shellSplit(raw)
	if err != nil {
		return nil, err
	}
	if len(argv) == 0 {
		return nil, fmt.Errorf("empty command")
	}

	unknown := map[string]struct{}{}
	for _, a := range argv {
		for _, tok := range extractTokens(a) {
			if _, ok := recordTokens[tok]; !ok {
				unknown[tok] = struct{}{}
			}
		}
	}
	if len(unknown) > 0 {
		names := make([]string, 0, len(unknown))
		for n := range unknown {
			names = append(names, "{"+n+"}")
		}
		sort.Strings(names)
		return nil, fmt.Errorf("unknown token(s): %s", strings.Join(names, ", "))
	}
	return &RecordTemplate{raw: raw, argv: argv}, nil
}

// Expand returns the final argv with every known token substituted.
// Called per-spawn; should not fail (parse-time validation already
// ruled out unknown tokens).
func (t *RecordTemplate) Expand(c TemplateContext) []string {
	out := make([]string, len(t.argv))
	for i, a := range t.argv {
		out[i] = substitute(a, c)
	}
	return out
}

// substitute replaces every {token} occurrence in s. Unknown tokens
// (which shouldn't reach here after parse-time validation) are left
// as-is. Matches only alphanumeric+underscore token names so literal
// "{other text}" in user templates isn't molested.
func substitute(s string, c TemplateContext) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		if s[i] != '{' {
			b.WriteByte(s[i])
			i++
			continue
		}
		end := strings.IndexByte(s[i+1:], '}')
		if end < 0 {
			b.WriteByte(s[i])
			i++
			continue
		}
		tok := s[i+1 : i+1+end]
		if !isValidTokenName(tok) {
			b.WriteByte(s[i])
			i++
			continue
		}
		if v, ok := resolve(tok, c); ok {
			b.WriteString(v)
		} else {
			b.WriteString(s[i : i+1+end+1]) // unknown — passthrough
		}
		i += 1 + end + 1
	}
	return b.String()
}

func resolve(tok string, c TemplateContext) (string, bool) {
	switch tok {
	case "cam_name":
		return c.CamName, true
	case "CAM_NAME":
		return strings.ToUpper(c.CamName), true
	case "rtsp_url":
		return fmt.Sprintf("rtsp://%s:%d/%s", c.RtspHost, c.RtspPort, c.CamName), true
	case "rtsp_host":
		return c.RtspHost, true
	case "rtsp_port":
		return fmt.Sprintf("%d", c.RtspPort), true
	case "output":
		return c.Output, true
	case "output_dir":
		if c.OutputDir != "" {
			return c.OutputDir, true
		}
		return filepath.Dir(c.Output), true
	case "output_stem":
		if c.OutputStem != "" {
			return c.OutputStem, true
		}
		return strings.TrimSuffix(c.Output, filepath.Ext(c.Output)), true
	case "segment_sec":
		return fmt.Sprintf("%d", c.SegmentSec), true
	case "quality":
		return c.Quality, true
	}
	return "", false
}

// extractTokens pulls every {tokenName} out of a single argv element.
// Used at parse time to validate the template.
func extractTokens(s string) []string {
	var out []string
	i := 0
	for i < len(s) {
		if s[i] != '{' {
			i++
			continue
		}
		end := strings.IndexByte(s[i+1:], '}')
		if end < 0 {
			break
		}
		tok := s[i+1 : i+1+end]
		if isValidTokenName(tok) {
			out = append(out, tok)
		}
		i += 1 + end + 1
	}
	return out
}

// isValidTokenName — tokens are [A-Za-z_][A-Za-z0-9_]*.
// Anything else (whitespace, punctuation, empty) is treated as
// literal text, not a token to validate.
func isValidTokenName(s string) bool {
	if len(s) == 0 {
		return false
	}
	for i, r := range s {
		alpha := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_'
		digit := r >= '0' && r <= '9'
		if i == 0 && !alpha {
			return false
		}
		if i > 0 && !alpha && !digit {
			return false
		}
	}
	return true
}

// shellSplit tokenizes a command line respecting double and single
// quotes. Inside double quotes, backslash escapes the next character.
// Single quotes are literal (no escape handling). Whitespace outside
// quotes separates arguments. This is a small subset of POSIX
// shell quoting — enough for honest use like
// `ffmpeg -i "{rtsp_url}" -c copy {output}`, not a full shell.
//
// Users who need pipes / variable expansion / subshells should
// prefix their template with `sh -c "..."` explicitly.
func shellSplit(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	inSingle, inDouble := false, false
	started := false

	flush := func() {
		if started {
			out = append(out, cur.String())
			cur.Reset()
			started = false
		}
	}

	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			} else {
				cur.WriteByte(c)
			}
		case inDouble:
			switch {
			case c == '\\' && i+1 < len(s):
				cur.WriteByte(s[i+1])
				i++
			case c == '"':
				inDouble = false
			default:
				cur.WriteByte(c)
			}
		default:
			switch c {
			case ' ', '\t', '\n':
				flush()
			case '\'':
				inSingle = true
				started = true
			case '"':
				inDouble = true
				started = true
			case '\\':
				if i+1 < len(s) {
					cur.WriteByte(s[i+1])
					i++
					started = true
				}
			default:
				cur.WriteByte(c)
				started = true
			}
		}
	}
	if inSingle || inDouble {
		return nil, fmt.Errorf("unterminated quote in command template")
	}
	flush()
	return out, nil
}
