package fastjson

import (
	"fmt"
	"strings"
)

// Detach returns a fully self-contained deep copy of v.
//
// Values returned from Parser.Parse*, Scanner.Value or Arena constructors
// are backed by the memory re-used by the corresponding Parser, Scanner,
// Arena or sync pool, so they become invalid after the next Parse call,
// Arena.Reset call or pool Put call.
//
// The Value returned from Detach doesn't share any mutable Value, Object,
// array or byte backing store with v, so it remains safe to read and modify
// after the original owner is re-used or reset. The original value remains
// valid and unchanged.
//
// Detach preserves the original object key order, string escapes and number
// literals byte-for-byte.
//
// Detach returns nil if v is nil.
func (v *Value) Detach() *Value {
	if v == nil {
		return nil
	}
	return detachValue(v)
}

func detachValue(v *Value) *Value {
	dst := &Value{t: v.t}
	switch v.t {
	case TypeObject:
		dst.o.keysUnescaped = v.o.keysUnescaped
		kvs := v.o.kvs
		dst.o.kvs = make([]kv, len(kvs))
		for i := range kvs {
			dst.o.kvs[i] = kv{
				k: strings.Clone(kvs[i].k),
				v: detachValue(kvs[i].v),
			}
		}
	case TypeArray:
		dst.a = make([]*Value, len(v.a))
		for i := range v.a {
			dst.a[i] = detachValue(v.a[i])
		}
	case typeRawString:
		// Preserve the raw (escaped) form, so the detached value
		// marshals to the same bytes as the original one.
		dst.s = strings.Clone(v.s)
	case TypeString, TypeNumber:
		dst.s = strings.Clone(v.s)
	case TypeTrue, TypeFalse, TypeNull:
		// Scalar singletons carry no mutable state; allocate fresh
		// Values anyway, so the result shares no Value with its source.
	default:
		panic(fmt.Errorf("BUG: unexpected Value type: %d", v.t))
	}
	return dst
}
