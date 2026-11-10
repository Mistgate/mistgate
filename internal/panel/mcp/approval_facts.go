//go:build !js

package mcp

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	approvalFactMaxRunes   = 900
	approvalFactChunkRunes = 820
)

func fullApprovalText(value string) (string, error) {
	scrubValue, validJoiners := ScrubValueWithoutJoiners(value)
	if !validJoiners {
		return "", fmt.Errorf("approval text contains hidden characters; remove them before planning")
	}
	if scrub(scrubValue) != scrubValue {
		return "", fmt.Errorf("approval text contains content the panel must redact; remove it before planning")
	}
	for _, r := range value {
		if (unicode.Is(unicode.Cf, r) && r != '\u200c' && r != '\u200d') || unicode.Is(unicode.Co, r) || (unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t') {
			return "", fmt.Errorf("approval text contains hidden characters; remove them before planning")
		}
	}
	return value, nil
}

// ScrubValueWithoutJoiners removes valid joiners so a credential cannot hide across them, and reports invalid joiner placement.
func ScrubValueWithoutJoiners(value string) (string, bool) {
	if !utf8.ValidString(value) {
		return "", false
	}
	runes := []rune(value)
	var clean strings.Builder
	clean.Grow(len(value))
	for i, r := range runes {
		if r != '\u200c' && r != '\u200d' {
			clean.WriteRune(r)
			continue
		}
		if i == 0 || i+1 == len(runes) || !approvalVisibleRune(runes[i-1]) || !approvalVisibleRune(runes[i+1]) {
			return "", false
		}
	}
	return clean.String(), true
}

func approvalVisibleRune(r rune) bool {
	if unicode.IsLetter(r) || unicode.IsMark(r) {
		return true
	}
	switch r {
	case 0x00a9, 0x00ae, 0x203c, 0x2049, 0x2122, 0x2139, 0x24c2, 0x3030, 0x303d, 0x3297, 0x3299:
		return true
	}
	return r >= 0x1f000 && r <= 0x1faff ||
		r >= 0x2194 && r <= 0x21aa ||
		r >= 0x2600 && r <= 0x27bf ||
		r >= 0x2300 && r <= 0x23ff ||
		r >= 0x25aa && r <= 0x25fe ||
		r >= 0x2934 && r <= 0x2935 ||
		r >= 0x2b00 && r <= 0x2bff
}

func approvalFact(key, value string, untrusted bool) (Fact, error) {
	var err error
	value, err = fullApprovalText(value)
	if err != nil {
		return Fact{}, fmt.Errorf("%s cannot be shown whole in approval: %w", key, err)
	}
	if utf8.RuneCountInString(value) > approvalFactMaxRunes {
		return Fact{}, fmt.Errorf("%s cannot be shown whole in the approval fact (limit %d characters)", key, approvalFactMaxRunes)
	}
	return Fact{Key: key, Value: value, Untrusted: untrusted}, nil
}

func approvalTextFacts(key, value string) ([]Fact, error) {
	var err error
	value, err = fullApprovalText(value)
	if err != nil {
		return nil, fmt.Errorf("%s cannot be shown whole in approval: %w", key, err)
	}
	runes := []rune(value)
	if len(runes) <= approvalFactMaxRunes {
		return []Fact{{Key: key, Value: value, Untrusted: true}}, nil
	}
	count := (len(runes) + approvalFactChunkRunes - 1) / approvalFactChunkRunes
	facts := make([]Fact, 0, count)
	for start, part := 0, 1; start < len(runes); part++ {
		end := min(start+approvalFactChunkRunes, len(runes))
		value := fmt.Sprintf("(%d/%d) %s", part, count, string(runes[start:end]))
		facts = append(facts, Fact{Key: key, Value: value, Untrusted: true})
		start = end
	}
	return facts, nil
}

func approvalTextChangeFacts(key, before, after string) ([]Fact, error) {
	oldFacts, err := approvalTextBeforeFacts(key+" before", before)
	if err != nil {
		return nil, err
	}
	newFacts, err := approvalTextFacts(key+" after", after)
	if err != nil {
		return nil, err
	}
	return append(oldFacts, newFacts...), nil
}

func approvalTextBeforeFacts(key, before string) ([]Fact, error) {
	before = strings.ToValidUTF8(before, "\uFFFD")
	before = removeFormatCharacters(before)
	before = scrub(before)
	var safeBefore strings.Builder
	for _, r := range before {
		if (unicode.Is(unicode.Cf, r) && r != '\u200c' && r != '\u200d') || unicode.Is(unicode.Co, r) || (unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t') {
			safeBefore.WriteRune('\uFFFD')
		} else {
			safeBefore.WriteRune(r)
		}
	}
	return approvalTextFacts(key, safeBefore.String())
}

func changedIDFacts(key string, before, after []string) []Fact {
	oldSet := make(map[string]struct{}, len(before))
	newSet := make(map[string]struct{}, len(after))
	for _, id := range before {
		oldSet[id] = struct{}{}
	}
	for _, id := range after {
		newSet[id] = struct{}{}
	}
	var removed, added []string
	for id := range oldSet {
		if _, ok := newSet[id]; !ok {
			removed = append(removed, id)
		}
	}
	for id := range newSet {
		if _, ok := oldSet[id]; !ok {
			added = append(added, id)
		}
	}
	if len(removed)+len(added) == 0 {
		return nil
	}
	// IDs are short generated panel identifiers. Sorting makes the approval stable across map iteration.
	slices.Sort(removed)
	slices.Sort(added)
	facts := []Fact{{Key: key, Value: fmt.Sprintf("%d removed, %d added, %d unchanged", len(removed), len(added), len(oldSet)-len(removed))}}
	for _, id := range removed {
		facts = append(facts, Fact{Key: key, Value: "removed: " + id, Untrusted: true})
	}
	for _, id := range added {
		facts = append(facts, Fact{Key: key, Value: "added: " + id, Untrusted: true})
	}
	return facts
}

func approvalFactsSize(facts []Fact, limit int, label string) error {
	encoded, err := marshal(facts)
	if err != nil {
		return fmt.Errorf("could not encode %s for approval", label)
	}
	if len(encoded) > limit {
		return fmt.Errorf("%s are too large to show whole in approval; shorten the change", label)
	}
	return nil
}
