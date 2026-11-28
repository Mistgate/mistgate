package subs

import "testing"

func TestReadableMihomoPreviewPreservesYAMLEscapes(t *testing.T) {
	const input = `# \U0001F30D
plain: \U0001F30D
single: '\U0001F30D'
block: |
  "\U0001F30D"
контроллер: [&name "\U0001F30D\"\\U0001F30D", *name, "\U0001F1EB\U0001F1EE"]
multiline: "before
  \U0001F5A5 after"
`
	const want = `# \U0001F30D
plain: \U0001F30D
single: '\U0001F30D'
block: |
  "\U0001F30D"
контроллер: [&name "🌍\"\\U0001F30D", *name, "🇫🇮"]
multiline: "before
  🖥 after"
`
	if got := readableMihomoPreview([]byte(input)); got != want {
		t.Errorf("preview YAML:\n%s\nwant:\n%s", got, want)
	}
}

func TestReadableMihomoPreviewSubdivisionFlags(t *testing.T) {
	const england = `\U000E0067\U000E0062\U000E0065\U000E006E\U000E0067\U000E007F`
	const scotland = `\U000e0067\U000e0062\U000e0073\U000e0063\U000e0074\U000e007f`
	const wales = `\U000E0067\U000E0062\U000E0077\U000E006C\U000E0073\U000E007F`
	const unknown = `\U000E0067\U000E0062\U000E0061\U000E0062\U000E0063\U000E007F`
	const incomplete = `\U000E0067\U000E0062\U000E0065\U000E006E\U000E0067`
	const black = `\U0001F3F4`
	const walesFlag = "🏴\U000e0067\U000e0062\U000e0077\U000e006c\U000e0073\U000e007f"
	for _, tc := range []struct {
		name, input, want string
	}{
		{"England", black + england, "🏴\U000e0067\U000e0062\U000e0065\U000e006e\U000e0067\U000e007f"},
		{"Scotland", black + scotland, "🏴\U000e0067\U000e0062\U000e0073\U000e0063\U000e0074\U000e007f"},
		{"Wales", black + wales, walesFlag},
		{"literal black flag", "🏴" + wales, walesFlag},
		{"standalone tags", england, england},
		{"unknown sequence", black + unknown, "🏴" + unknown},
		{"no cancel tag", black + incomplete, "🏴" + incomplete},
		{"literal backslash before flag", `\` + black + england, `\` + black + england},
		{"literal backslash after flag", black + `\` + wales, "🏴" + `\` + wales},
		{"separate scalars", black + `", "` + england, "🏴" + `", "` + england},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := `label: ["` + tc.input + "\"]\n"
			want := `label: ["` + tc.want + "\"]\n"
			if got := readableMihomoPreview([]byte(input)); got != want {
				t.Errorf("preview YAML: %q, want %q", got, want)
			}
		})
	}
}
