package auth

import (
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// A dry-run call is opened by a flag of the request, so every row of the table has to name a real bool flag of its own message,
// and has to sit on a procedure that only the owner's approval opens (a row on any other procedure would never be asked).
func TestPlanningRequestsAreChecksOfApprovedProcedures(t *testing.T) {
	for path, row := range planningRequests {
		if access, ok := TokenAccess(path); !ok || access != TokenAccessApproved {
			t.Errorf("%s has a dry-run row and token access %d (listed %v), want approved", path, access, ok)
		}
		m := row.message()
		if row.dry(m) {
			t.Errorf("%s: an empty request is a dry run", path)
		}
		fields := m.ProtoReflect().Descriptor().Fields()
		flag := fields.ByName("dry_run")
		if flag == nil {
			flag = fields.ByName("validate_only")
		}
		if flag == nil || flag.Kind() != protoreflect.BoolKind {
			t.Errorf("%s: %s has no dry_run or validate_only flag", path, m.ProtoReflect().Descriptor().FullName())
			continue
		}
		m.ProtoReflect().Set(flag, protoreflect.ValueOfBool(true))
		if !row.dry(m) {
			t.Errorf("%s: the flag %s does not make the call a dry run", path, flag.Name())
		}
	}
}
