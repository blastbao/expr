package deref

import (
	"fmt"
	"reflect"
)

func Interface(p any) any {
	if p == nil {
		return nil
	}

	v := Value(reflect.ValueOf(p))
	if v.IsValid() {
		return v.Interface()
	}

	panic(fmt.Sprintf("cannot dereference %v", p))
}

// Type 对 t 进行指针的循环解引用，直到拿到实际的类型。
func Type(t reflect.Type) reflect.Type {
	if t == nil {
		return nil
	}
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t
}

// Value 对 v 进行指针的循环解引用，直到拿到实际的值。
func Value(v reflect.Value) reflect.Value {
	for v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return v
		}
		v = v.Elem()
	}
	return v
}
