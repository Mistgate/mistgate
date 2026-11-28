package subsettings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

const mihomoDirective = "# @mistgate-"

// Bound the combined validation work, not just the size of the stored source or the number of groups.
const maxMihomoVariantBytes = 16 * MaxMihomoTemplateBytes

type mihomoGroupBlock struct {
	start, body, end, after int // byte offsets: opening mark, body, closing mark, next line
	parent, next, line      int // opening-order indices; next skips the entire subtree
	ids                     []string
}

// A marker after any YAML line break is still reserved. Reject unusual breaks anywhere in a conditional
// source, including raw CRs in JSON arguments, but hand legacy YAML to the parser unchanged.
func mihomoUnusualLineBreak(text string) int {
	if !strings.ContainsAny(text, "\r\u0085\u2028\u2029") {
		return 0
	}
	first, marked := 0, false
	for line := 1; text != ""; line++ {
		marked = marked || strings.HasPrefix(strings.TrimLeft(text, "\ufeff"), mihomoDirective)
		if marked && first != 0 {
			return first
		}
		i := strings.IndexAny(text, "\r\n\u0085\u2028\u2029")
		if i < 0 {
			break
		}
		text = text[i:]
		switch {
		case strings.HasPrefix(text, "\r\n"):
			text = text[2:]
		case text[0] == '\n':
			text = text[1:]
		default:
			if first == 0 {
				first = line
			}
			if marked {
				return first
			}
			_, size := utf8.DecodeRuneInString(text)
			text = text[size:]
		}
	}
	return 0
}

func mihomoGroupBlocks(text string) ([]mihomoGroupBlock, error) {
	if !strings.Contains(text, mihomoDirective) {
		return nil, nil
	}
	if line := mihomoUnusualLineBreak(text); line != 0 {
		return nil, fmt.Errorf("line %d: group blocks support only LF and CRLF line breaks", line)
	}
	var blocks []mihomoGroupBlock
	open := -1
	for offset, number := 0, 1; offset < len(text); number++ {
		line, rest, lf := strings.Cut(text[offset:], "\n")
		after := len(text) - len(rest)
		if lf {
			line = strings.TrimSuffix(line, "\r")
		}
		// yaml.v3 skips a BOM at the start of a line. Never let that turn a condition into a plain comment.
		if strings.HasPrefix(line, "\ufeff") && strings.HasPrefix(strings.TrimLeft(line, "\ufeff"), mihomoDirective) {
			return nil, fmt.Errorf("line %d: a group marker must not start with a byte order mark", number)
		}
		directive, marked := strings.CutPrefix(line, mihomoDirective)
		if marked {
			word, args := directive, ""
			if i := strings.IndexAny(directive, " \t"); i >= 0 {
				word, args = directive[:i], strings.Trim(directive[i:], " \t")
			}
			switch word {
			case "if":
				var ids []string
				if json.Unmarshal([]byte(args), &ids) != nil || len(ids) == 0 {
					return nil, fmt.Errorf("line %d: # @mistgate-if requires a non-empty JSON array of group IDs", number)
				}
				if len(ids) > maxHeaderGroups {
					return nil, fmt.Errorf("line %d: # @mistgate-if accepts at most %d group IDs", number, maxHeaderGroups)
				}
				seen := make(map[string]bool, len(ids))
				for _, id := range ids {
					if !idChars(id) || seen[id] {
						return nil, fmt.Errorf("line %d: # @mistgate-if has a repeated or malformed group ID", number)
					}
					seen[id] = true
				}
				blocks = append(blocks, mihomoGroupBlock{start: offset, body: after, ids: ids, parent: open, line: number})
				open = len(blocks) - 1
			case "end":
				if args != "" {
					return nil, fmt.Errorf("line %d: # @mistgate-end does not take arguments", number)
				}
				if open < 0 {
					return nil, fmt.Errorf("line %d: # @mistgate-end has no matching # @mistgate-if", number)
				}
				block := &blocks[open]
				block.end, block.after, block.next = offset, after, len(blocks)
				open = block.parent
			default:
				return nil, fmt.Errorf("line %d: unknown # @mistgate- directive", number)
			}
		}
		offset = after
	}
	if open >= 0 {
		return nil, fmt.Errorf("line %d: # @mistgate-if has no matching # @mistgate-end", blocks[open].line)
	}
	return blocks, nil
}

type mihomoVariant struct {
	text   string
	blocks []mihomoGroupBlock
	keep   func(int) bool // nil selects common YAML
}

// omissions yields ordered, non-overlapping ranges of whole source lines. Jumping over an excluded
// block excludes all its children, regardless of their conditions.
func (v mihomoVariant) omissions(yield func(start, end int) bool) {
	open := -1
	for i := 0; i < len(v.blocks) || open >= 0; {
		if open >= 0 && (i == len(v.blocks) || v.blocks[open].next == i) {
			b := v.blocks[open]
			if !yield(b.end, b.after) {
				return
			}
			open = b.parent
			continue
		}
		b := v.blocks[i]
		if v.keep != nil && v.keep(i) {
			if !yield(b.start, b.body) {
				return
			}
			open, i = i, i+1
		} else {
			if !yield(b.start, b.after) {
				return
			}
			i = b.next
		}
	}
}

func (v mihomoVariant) size() int {
	size := len(v.text)
	for start, end := range v.omissions {
		size -= end - start
	}
	return size
}

func (v mihomoVariant) resolve(size int) string {
	if len(v.blocks) == 0 {
		return v.text
	}
	var out strings.Builder
	out.Grow(size)
	at := 0
	for start, end := range v.omissions {
		out.WriteString(v.text[at:start])
		at = end
	}
	out.WriteString(v.text[at:])
	return out.String()
}

// sourceError builds the line map only for a failed validation, never for a served or valid variant.
func (v mihomoVariant) sourceError(err error) error {
	if len(v.blocks) == 0 {
		return err
	}
	type jump struct{ line, skipped int }
	var jumps []jump
	at, line, skipped := 0, 1, 0
	for start, end := range v.omissions {
		if end == len(v.text) { // EOF diagnostics point to the start of the trailing omission.
			break
		}
		line += strings.Count(v.text[at:start], "\n")
		skipped += strings.Count(v.text[start:end], "\n")
		jumps = append(jumps, jump{line, skipped})
		at = end
	}
	sourceLine := func(line int) int {
		i := sort.Search(len(jumps), func(i int) bool { return jumps[i].line > line })
		if i != 0 {
			return line + jumps[i-1].skipped
		}
		return line
	}
	if diagnostic, ok := err.(mihomoYAMLDiagnostic); ok {
		return errors.New(diagnostic.message(sourceLine))
	}
	return errors.New(mihomoDiagnosticLines(err.Error(), sourceLine))
}

// ResolveMihomoGroup selects the blocks for one primary group (or just the common YAML for an empty ID).
// Markers and excluded lines are omitted, with all other bytes preserved. The caller decides whether the template is assigned
// to the group before passing its ID.
func ResolveMihomoGroup(text, groupID string) (string, error) {
	blocks, err := mihomoGroupBlocks(text)
	if err != nil {
		return "", err
	}
	if len(blocks) == 0 {
		return text, nil
	}
	variant := mihomoVariant{text: text, blocks: blocks}
	if groupID != "" {
		variant.keep = func(i int) bool { return slices.Contains(blocks[i].ids, groupID) }
	}
	return variant.resolve(variant.size()), nil
}

// visitMihomoVariants passes the common variant followed by each distinct pattern of enabled blocks in assignment order.
// A bitmap per group records only the selections; each resolved YAML document is used and discarded in turn.
func visitMihomoVariants(ctx context.Context, text string, groupIDs []string, visit func(groupID, resolved string, variant mihomoVariant) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(text) > MaxMihomoTemplateBytes {
		return fmt.Errorf("%d bytes, at most %d", len(text), MaxMihomoTemplateBytes)
	}
	if !utf8.ValidString(text) {
		return errors.New("the text is not valid UTF-8")
	}
	if len(groupIDs) > maxHeaderGroups {
		return fmt.Errorf("at most %d assigned group IDs", maxHeaderGroups)
	}
	blocks, err := mihomoGroupBlocks(text)
	if err != nil {
		return err
	}
	work := 0
	check := func(id string, keep func(int) bool) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		variant := mihomoVariant{text: text, blocks: blocks, keep: keep}
		size := variant.size()
		work += size
		if work > maxMihomoVariantBytes {
			return fmt.Errorf("group variants exceed the validation limit of %d resolved YAML bytes", maxMihomoVariantBytes)
		}
		err := visit(id, variant.resolve(size), variant)
		if canceled := ctx.Err(); canceled != nil {
			return canceled
		}
		return err
	}
	if err := check("", nil); err != nil {
		return err
	}
	if len(blocks) == 0 || len(groupIDs) == 0 {
		return nil
	}
	width := (len(blocks) + 7) / 8
	masks := make([]byte, (len(groupIDs)+1)*width) // the initial empty mask represents common YAML
	assigned := make(map[string]int, len(groupIDs))
	for i, id := range groupIDs {
		if id != "" {
			assigned[id] = (i + 1) * width
		}
	}
	for i, block := range blocks {
		for _, id := range block.ids {
			if start, ok := assigned[id]; ok && (block.parent < 0 || masks[start+block.parent/8]&(1<<(block.parent%8)) != 0) {
				masks[start+i/8] |= 1 << (i % 8)
			}
		}
	}
	seen := map[string]bool{string(masks[:width]): true}
	for i, id := range groupIDs {
		start := (i + 1) * width
		mask := masks[start : start+width]
		if seen[string(mask)] {
			continue
		}
		seen[string(mask)] = true
		if err := check(id, func(i int) bool { return mask[i/8]&(1<<(i%8)) != 0 }); err != nil {
			return err
		}
	}
	return nil
}

// CheckMihomoGroups checks the common YAML and every distinct variant served to an assigned primary group.
func CheckMihomoGroups(ctx context.Context, text string, groupIDs []string, autoOn bool) error {
	return visitMihomoVariants(ctx, text, groupIDs, func(groupID, resolved string, variant mihomoVariant) error {
		// A conditional source can disappear entirely. Keep its missing-document diagnostic distinct
		// from that of a completely empty source.
		var err error
		if resolved == "" && len(variant.blocks) != 0 {
			err = errors.New("no YAML document")
		} else {
			err = checkMihomoTemplate(ctx, resolved, autoOn, variant.sourceError)
		}
		if err != nil {
			if groupID == "" {
				return fmt.Errorf("common: %w", err)
			}
			return fmt.Errorf("group %q: %w", groupID, err)
		}
		return nil
	})
}

// MihomoGroupWarnings returns the distinct warnings from the common YAML and the assigned variants, in discovery order.
// A source with malformed directives gives no warnings; CheckMihomoGroups reports the refusal.
func MihomoGroupWarnings(text string, groupIDs []string, autoOn bool) []MihomoWarning {
	var out []MihomoWarning
	seen := map[MihomoWarning]bool{}
	_ = visitMihomoVariants(context.Background(), text, groupIDs, func(_, resolved string, _ mihomoVariant) error {
		for _, w := range MihomoTemplateWarnings(resolved, autoOn) {
			if !seen[w] {
				seen[w] = true
				out = append(out, w)
			}
		}
		return nil
	})
	return out
}
