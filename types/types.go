package types

import (
	"fmt"
	"reflect"
	"strings"

	. "github.com/expr-lang/expr/checker/nature"
)

// Type is a type that can be used to represent a value.
type Type interface {
	Nature() Nature  // 返回类型的 Nature 元信息
	Equal(Type) bool // 判断两个 Type 是否相等
	String() string  // 返回字符串形式的类型名
}

var (
	Int     = TypeOf(0)
	Int8    = TypeOf(int8(0))
	Int16   = TypeOf(int16(0))
	Int32   = TypeOf(int32(0))
	Int64   = TypeOf(int64(0))
	Uint    = TypeOf(uint(0))
	Uint8   = TypeOf(uint8(0))
	Uint16  = TypeOf(uint16(0))
	Uint32  = TypeOf(uint32(0))
	Uint64  = TypeOf(uint64(0))
	Float   = TypeOf(float32(0))
	Float64 = TypeOf(float64(0))
	String  = TypeOf("")
	Bool    = TypeOf(true)
	Nil     = nilType{}
	Any     = anyType{}
)

func TypeOf(v any) Type {
	if v == nil {
		return Nil
	}
	return rtype{t: reflect.TypeOf(v)}
}

// anyType 表示任意类型（Go 中的 interface{}）
type anyType struct{}

func (anyType) Nature() Nature {
	return Nature{Type: nil}
} // 返回空，没有特定类型

func (anyType) Equal(t Type) bool {
	return true
} // any 能和任意类型匹配，Equal 永远返回 true

func (anyType) String() string {
	return "any"
}

// nilType 表示空类型
type nilType struct{}

func (nilType) Nature() Nature {
	return Nature{Nil: true}
}

func (nilType) Equal(t Type) bool { // nilType 与 Any 或其它 nilType 相等
	if t == Any {
		return true
	}
	return t == Nil
}

func (nilType) String() string {
	return "nil"
}

// rtype 是 reflect.Type 的直接封装
type rtype struct {
	t reflect.Type
}

func (r rtype) Nature() Nature {
	return Nature{Type: r.t}
}

func (r rtype) Equal(t Type) bool {
	if t == Any {
		return true
	}
	if rt, ok := t.(rtype); ok {
		return r.t.String() == rt.t.String()
	}
	return false
}

func (r rtype) String() string {
	return r.t.String()
}

// Map represents a map[string]any type with defined keys.
//
// Map 是一种增强版的 map[string]any ，允许指定每个 key 的类型。
type Map map[string]Type

const Extra = "[[__extra_keys__]]" // 标记 Map 是否允许额外的未知 key（非严格模式）

func (m Map) Nature() Nature {
	nt := Nature{
		Type:   reflect.TypeOf(map[string]any{}),
		Fields: make(map[string]Nature, len(m)), // 存储已确定的 key 的 Nature 信息
		Strict: true,                            // 严格模式（默认）
	}
	for k, v := range m {
		if k == Extra { // 如果允许额外的未知 key ，意味着非严格模式，设置 DefaultMapValue 表示这些 key 的值类型。
			nt.Strict = false
			natureOfDefaultValue := v.Nature()
			nt.DefaultMapValue = &natureOfDefaultValue
			continue
		}
		nt.Fields[k] = v.Nature()
	}
	return nt
}

func (m Map) Equal(t Type) bool {
	if t == Any {
		return true
	}
	mt, ok := t.(Map)
	if !ok {
		return false
	}
	if len(m) != len(mt) {
		return false
	}
	for k, v := range m {
		if !v.Equal(mt[k]) {
			return false
		}
	}
	return true
}

func (m Map) String() string {
	pairs := make([]string, 0, len(m))
	for k, v := range m {
		pairs = append(pairs, fmt.Sprintf("%s: %s", k, v.String()))
	}
	return fmt.Sprintf("Map{%s}", strings.Join(pairs, ", "))
}

// Array returns a type that represents an array of the given type.
func Array(of Type) Type {
	return array{of}
}

type array struct {
	of Type
}

func (a array) Nature() Nature {
	of := a.of.Nature()
	return Nature{
		Type:    reflect.TypeOf([]any{}),
		Fields:  make(map[string]Nature, 1),
		ArrayOf: &of,
	}
}

func (a array) Equal(t Type) bool {
	if t == Any {
		return true
	}
	at, ok := t.(array)
	if !ok {
		return false
	}
	if a.of.Equal(at.of) {
		return true
	}
	return false
}

func (a array) String() string {
	return fmt.Sprintf("Array{%s}", a.of.String())
}
