package access

import (
	"encoding/base64"
	"errors"
	"strings"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protoreflect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// listUsersInteger reads a field by its descriptor when the generated code includes it. The wire fallback keeps this
// source usable before the proto-generated files are refreshed; protobuf preserves these additive unknown fields.
func listUsersInteger(m *adminv1.ListUsersRequest, name protoreflect.Name, number protowire.Number) (uint64, bool, error) {
	msg := m.ProtoReflect()
	if field := msg.Descriptor().Fields().ByName(name); field != nil {
		switch field.Kind() {
		case protoreflect.EnumKind:
			return uint64(msg.Get(field).Enum()), true, nil
		case protoreflect.BoolKind:
			if msg.Get(field).Bool() {
				return 1, true, nil
			}
			return 0, true, nil
		default:
			return 0, false, errListUsersWireField
		}
	}
	return listUsersUnknownInteger(msg.GetUnknown(), number)
}

var errListUsersWireField = protowire.ParseError(-1)

const userListPageTokenPrefix = "\x00mistgate-user-list-v1:"

var (
	errUserListPageTokenFormat    = errors.New("bad user list page token")
	errUserListPageTokenDirection = errors.New("user list page token has a different name direction")
)

func encodeUserListPageToken(name string, descending bool) string {
	direction := "a"
	if descending {
		direction = "d"
	}
	return base64.RawURLEncoding.EncodeToString([]byte(userListPageTokenPrefix + direction + name))
}

func decodeUserListPageToken(token string, descending bool) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", errUserListPageTokenFormat
	}
	payload := string(raw)
	if strings.HasPrefix(payload, userListPageTokenPrefix) {
		rest := strings.TrimPrefix(payload, userListPageTokenPrefix)
		if len(rest) < 1 || (rest[0] != 'a' && rest[0] != 'd') {
			return "", errUserListPageTokenFormat
		}
		if (rest[0] == 'd') != descending {
			return "", errUserListPageTokenDirection
		}
		return rest[1:], nil
	}
	// Earlier versions encoded only the name. Keep those cursors valid for ascending name order.
	if descending {
		return "", errUserListPageTokenDirection
	}
	return payload, nil
}

func listUsersUnknownInteger(unknown []byte, number protowire.Number) (uint64, bool, error) {
	var value uint64
	found := false
	for len(unknown) != 0 {
		fieldNumber, wireType, n := protowire.ConsumeTag(unknown)
		if n < 0 {
			return 0, false, protowire.ParseError(n)
		}
		unknown = unknown[n:]
		if fieldNumber == number {
			if wireType != protowire.VarintType {
				return 0, false, errListUsersWireField
			}
			parsed, n := protowire.ConsumeVarint(unknown)
			if n < 0 {
				return 0, false, protowire.ParseError(n)
			}
			unknown = unknown[n:]
			value = parsed
			found = true
			continue
		}
		n = protowire.ConsumeFieldValue(fieldNumber, wireType, unknown)
		if n < 0 {
			return 0, false, protowire.ParseError(n)
		}
		unknown = unknown[n:]
	}
	return value, found, nil
}

func setListUserCountField(counts *adminv1.UserCounts, name protoreflect.Name, number protowire.Number, value uint32) {
	msg := counts.ProtoReflect()
	if field := msg.Descriptor().Fields().ByName(name); field != nil && field.Kind() == protoreflect.Uint32Kind {
		msg.Set(field, protoreflect.ValueOfUint32(value))
		return
	}
	unknown := msg.GetUnknown()
	unknown = protowire.AppendTag(unknown, number, protowire.VarintType)
	unknown = protowire.AppendVarint(unknown, uint64(value))
	msg.SetUnknown(unknown)
}
