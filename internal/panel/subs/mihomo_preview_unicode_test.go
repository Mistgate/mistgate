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
