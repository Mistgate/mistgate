package subsettings

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

const mihomoDirective = "# @mistgate-"

type mihomoGroupBlock struct {
	start, body, end, after int // byte offsets: opening mark, body, closing mark, next line
	ids                     []string
}

func mihomoGroupBlocks(text string) ([]mihomoGroupBlock, error) {
	if !strings.Contains(text, mihomoDirective) {
		return nil, nil
	}
	var blocks []mihomoGroupBlock
	var open mihomoGroupBlock
	openLine := 0
	for offset, number := 0, 1; offset < len(text); number++ {
		line, rest, lf := strings.Cut(text[offset:], "\n")
		after := len(text) - len(rest)
		if lf {
			line = strings.TrimSuffix(line, "\r")
		}
		directive, marked := strings.CutPrefix(line, mihomoDirective)
		if marked {
			word, args := directive, ""
			if i := strings.IndexAny(directive, " \t"); i >= 0 {
				word, args = directive[:i], strings.Trim(directive[i:], " \t")
			}
			switch word {
			case "if":
				if openLine != 0 {
					return nil, fmt.Errorf("line %d: nested # @mistgate-if (block opened on line %d)", number, openLine)
				}
				var ids []string
				if strings.ContainsRune(args, '\r') || json.Unmarshal([]byte(args), &ids) != nil || len(ids) == 0 {
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
				open = mihomoGroupBlock{start: offset, body: after, ids: ids}
				openLine = number
			case "end":
				if args != "" {
					return nil, fmt.Errorf("line %d: # @mistgate-end does not take arguments", number)
				}
				if openLine == 0 {
					return nil, fmt.Errorf("line %d: # @mistgate-end has no matching # @mistgate-if", number)
				}
				open.end, open.after = offset, after
				blocks = append(blocks, open)
				openLine = 0
			default:
				return nil, fmt.Errorf("line %d: unknown # @mistgate- directive", number)
			}
		}
		offset = after
	}
	if openLine != 0 {
		return nil, fmt.Errorf("line %d: # @mistgate-if has no matching # @mistgate-end", openLine)
	}
	return blocks, nil
}

// blankMihomoLines discards the line contents, keeping the original LF or CRLF in place.
func blankMihomoLines(out *strings.Builder, text string) {
	for {
		i := strings.IndexByte(text, '\n')
		if i < 0 {
			return
		}
		if i > 0 && text[i-1] == '\r' {
			out.WriteByte('\r')
		}
		out.WriteByte('\n')
		text = text[i+1:]
	}
}

// A nil keep selects the common variant.
func resolveMihomoBlocks(text string, blocks []mihomoGroupBlock, keep func(int) bool) string {
	if len(blocks) == 0 {
		return text
	}
	var out strings.Builder
	out.Grow(len(text))
	after := 0
	for i, b := range blocks {
		out.WriteString(text[after:b.start])
		if keep != nil && keep(i) {
			blankMihomoLines(&out, text[b.start:b.body])
			out.WriteString(text[b.body:b.end])
			blankMihomoLines(&out, text[b.end:b.after])
		} else {
			blankMihomoLines(&out, text[b.start:b.after])
		}
		after = b.after
	}
	out.WriteString(text[after:])
	return out.String()
}

// ResolveMihomoGroup selects the blocks for one primary group (or just the common YAML for an empty ID), keeping source line
// numbers. The caller decides whether the template is assigned to the group before passing its ID.
func ResolveMihomoGroup(text, groupID string) (string, error) {
	blocks, err := mihomoGroupBlocks(text)
	if err != nil {
		return "", err
	}
	if groupID == "" {
		return resolveMihomoBlocks(text, blocks, nil), nil
	}
	return resolveMihomoBlocks(text, blocks, func(i int) bool { return slices.Contains(blocks[i].ids, groupID) }), nil
}

// visitMihomoVariants passes the common variant followed by each distinct pattern of enabled blocks in assignment order.
// A bitmap per group records only the selections; each resolved YAML document is used and discarded in turn.
func visitMihomoVariants(text string, groupIDs []string, visit func(groupID, resolved string) error) error {
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
	if err := visit("", resolveMihomoBlocks(text, blocks, nil)); err != nil {
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
			if start, ok := assigned[id]; ok {
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
		resolved := resolveMihomoBlocks(text, blocks, func(i int) bool { return mask[i/8]&(1<<(i%8)) != 0 })
		if err := visit(id, resolved); err != nil {
			return err
		}
	}
	return nil
}

// CheckMihomoGroups checks the common YAML and every distinct variant served to an assigned primary group.
func CheckMihomoGroups(text string, groupIDs []string, autoOn bool) error {
	return visitMihomoVariants(text, groupIDs, func(groupID, resolved string) error {
		if err := CheckMihomoTemplate(resolved, autoOn); err != nil {
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
	_ = visitMihomoVariants(text, groupIDs, func(_, resolved string) error {
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
