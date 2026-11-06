package release

import (
	"regexp"

	"golang.org/x/mod/semver"
)

// Channel is the release cadence represented by a release tag.
type Channel string

const (
	ChannelStable  Channel = "stable"
	ChannelTesting Channel = "testing"
	ChannelDev     Channel = "dev"
)

var (
	channelTagPattern = regexp.MustCompile(`^v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-(?:testing|dev)\.[1-9][0-9]*)?$`)
	testingTagPattern = regexp.MustCompile(`-testing\.[1-9][0-9]*$`)
	devTagPattern     = regexp.MustCompile(`-dev\.[1-9][0-9]*$`)
)

// ParseChannel parses a channel name used in settings and API requests.
func ParseChannel(value string) (Channel, bool) {
	c := Channel(value)
	return c, c.Valid()
}

// Valid reports whether c is one of the supported release channels.
func (c Channel) Valid() bool {
	return c == ChannelStable || c == ChannelTesting || c == ChannelDev
}

// Allows reports whether a panel on c may see releases from releaseChannel. A channel sees itself and every more
// stable channel: stable < testing < dev.
func (c Channel) Allows(releaseChannel Channel) bool {
	return c.Valid() && releaseChannel.Valid() && channelRank(releaseChannel) <= channelRank(c)
}

func channelRank(c Channel) int {
	switch c {
	case ChannelStable:
		return 0
	case ChannelTesting:
		return 1
	case ChannelDev:
		return 2
	default:
		return -1
	}
}

// ChannelOf returns the channel encoded by a canonical Mistgate release tag. Only vX.Y.Z, vX.Y.Z-testing.N and
// vX.Y.Z-dev.N are release tags; N starts at 1 and build metadata is not accepted.
func ChannelOf(tag string) (Channel, bool) {
	if !channelTagPattern.MatchString(tag) || !semver.IsValid(tag) || semver.Canonical(tag) != tag {
		return "", false
	}
	switch {
	case testingTagPattern.MatchString(tag):
		return ChannelTesting, true
	case devTagPattern.MatchString(tag):
		return ChannelDev, true
	default:
		return ChannelStable, true
	}
}

// ValidChannelTag reports whether tag is a supported canonical release tag.
func ValidChannelTag(tag string) bool {
	_, ok := ChannelOf(tag)
	return ok
}

// LatestTagForChannel picks the highest semver tag the channel may see. Invalid tags are ignored.
func LatestTagForChannel(tags []string, channel Channel) (string, bool) {
	if !channel.Valid() {
		return "", false
	}
	latest := ""
	for _, tag := range tags {
		tagChannel, ok := ChannelOf(tag)
		if !ok || !channel.Allows(tagChannel) {
			continue
		}
		if latest == "" || semver.Compare(tag, latest) > 0 {
			latest = tag
		}
	}
	return latest, latest != ""
}
