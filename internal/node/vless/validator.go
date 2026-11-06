package vless

import (
	"errors"
	"fmt"
	"reflect"
	"unsafe"

	"github.com/xtls/xray-core/common/protocol"
	xuuid "github.com/xtls/xray-core/common/uuid"
	xrayvless "github.com/xtls/xray-core/proxy/vless"
)

type countingValidator struct {
	xrayvless.Validator
	onUnknown func()
}

func (v *countingValidator) Get(id xuuid.UUID) *protocol.MemoryUser {
	user := v.Validator.Get(id)
	if user == nil {
		v.onUnknown()
	}
	return user
}

// instrumentVLESSValidator wraps the validator held by this inbound's Xray VLESS handler. Xray exposes the handler as a
// UserManager but does not expose its Validator setter; the field is an interface specifically used by both request
// decoding and AddUser/RemoveUser. The module version is pinned, and the runtime shape checks return an error, without
// touching the handler, if Xray changes this seam: the caller then serves the inbound without the count. The wrapper
// forwards every UserManager-backed validator operation unchanged.
func instrumentVLESSValidator(inbound any, onUnknown func()) error {
	value := reflect.ValueOf(inbound)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		return errors.New("xray VLESS inbound is not a handler pointer")
	}
	typ := value.Type().Elem()
	if typ.PkgPath() != "github.com/xtls/xray-core/proxy/vless/inbound" || typ.Name() != "Handler" {
		return fmt.Errorf("unexpected xray VLESS inbound type %s", typ)
	}
	field := value.Elem().FieldByName("validator")
	if !field.IsValid() || field.Kind() != reflect.Interface || !field.CanAddr() {
		return errors.New("xray VLESS handler validator field is unavailable")
	}
	validator, ok := reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().Interface().(xrayvless.Validator)
	if !ok || validator == nil {
		return errors.New("xray VLESS handler validator has an unexpected type")
	}
	wrapped := &countingValidator{Validator: validator, onUnknown: onUnknown}
	reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().Set(reflect.ValueOf(wrapped))
	return nil
}
