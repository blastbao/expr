package builtin

import (
	"fmt"
	"reflect"
	"time"

	"github.com/expr-lang/expr/internal/deref"
)

var (
	anyType      = reflect.TypeOf(new(any)).Elem()
	integerType  = reflect.TypeOf(0)
	floatType    = reflect.TypeOf(float64(0))
	arrayType    = reflect.TypeOf([]any{})
	mapType      = reflect.TypeOf(map[any]any{})
	timeType     = reflect.TypeOf(new(time.Time)).Elem()
	locationType = reflect.TypeOf(new(time.Location))
)

func kind(t reflect.Type) reflect.Kind {
	if t == nil {
		return reflect.Invalid
	}
	t = deref.Type(t)
	return t.Kind()
}

// 接收任意数量的参数，将它们转换为反射类型（reflect.Type）并验证这些类型是否为函数，最终返回一个包含这些函数反射类型的切片。
func types(types ...any) []reflect.Type {
	ts := make([]reflect.Type, len(types))
	for i, t := range types {
		t := reflect.TypeOf(t)       // 获取参数的反射类型
		if t.Kind() == reflect.Ptr { // 如果类型是指针，则通过 t.Elem() 获取指针指向的实际类型
			t = t.Elem()
		}
		if t.Kind() != reflect.Func { // 检查类型是否为函数（reflect.Func）
			panic("not a function")
		}
		ts[i] = t // 将的函数类型存入切片 ts 中
	}
	return ts
}

func toInt(val any) (int, error) {
	switch v := val.(type) {
	case int:
		return v, nil
	case int8:
		return int(v), nil
	case int16:
		return int(v), nil
	case int32:
		return int(v), nil
	case int64:
		return int(v), nil
	case uint:
		return int(v), nil
	case uint8:
		return int(v), nil
	case uint16:
		return int(v), nil
	case uint32:
		return int(v), nil
	case uint64:
		return int(v), nil
	default:
		return 0, fmt.Errorf("cannot use %T as argument (type int)", val)
	}
}

func bitFunc(name string, fn func(x, y int) (any, error)) *Function {
	return &Function{
		Name: name,
		Func: func(args ...any) (any, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("invalid number of arguments for %s (expected 2, got %d)", name, len(args))
			}
			x, err := toInt(args[0])
			if err != nil {
				return nil, fmt.Errorf("%v to call %s", err, name)
			}
			y, err := toInt(args[1])
			if err != nil {
				return nil, fmt.Errorf("%v to call %s", err, name)
			}
			return fn(x, y)
		},
		Types: types(new(func(int, int) int)),
	}
}
