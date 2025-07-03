package nature

import (
	"reflect"

	"github.com/expr-lang/expr/builtin"
	"github.com/expr-lang/expr/internal/deref"
)

var (
	unknown = Nature{}
)

type Nature struct {
	Type            reflect.Type      // Type of the value. If nil, then value is unknown.
	Func            *builtin.Function // Used to pass function type from callee to CallNode.
	ArrayOf         *Nature           // Elem nature of array type (usually Type is []any, but ArrayOf can be any nature).
	PredicateOut    *Nature           // Out nature of predicate.
	Fields          map[string]Nature // Fields of map type.
	DefaultMapValue *Nature           // Default value of map type.
	Strict          bool              // If map is types.StrictMap.
	Nil             bool              // If value is nil.
	Method          bool              // If value retrieved from method. Usually used to determine amount of in arguments.
	MethodIndex     int               // Index of method in type.
	FieldIndex      []int             // Index of field in type.
}

type Comments struct {
	Type            reflect.Type      // 值的类型（比如 int、[]string、map[string]int）
	Func            *builtin.Function // 如果是内置函数，这里保留函数定义信息
	ArrayOf         *Nature           // 如果是数组/切片，其元素类型
	PredicateOut    *Nature           // 如果是谓词函数，返回类型（常用于过滤场景）
	Fields          map[string]Nature // 如果是 Map，用于描述键 -> 值类型映射
	DefaultMapValue *Nature           // Map 默认值类型（没有 key 命中时）
	Strict          bool              // 是否为严格 map（键固定，不能任意添加）
	Nil             bool              // 是否为 nil 值
	Method          bool              // 是否是通过 MethodByName 获得的 method
	MethodIndex     int               // 方法索引（对应 reflect.Type.Method(index)）
	FieldIndex      []int             // 如果是 struct 字段，其 index 路径（嵌套字段）
}

func (n Nature) IsAny() bool {
	return n.Kind() == reflect.Interface && n.NumMethods() == 0
}

func (n Nature) IsUnknown() bool {
	switch {
	case n.Type == nil && !n.Nil:
		return true
	case n.IsAny():
		return true
	}
	return false
}

func (n Nature) String() string {
	if n.Type != nil {
		return n.Type.String()
	}
	return "unknown"
}

// Deref 解引用，得到最底层类型
func (n Nature) Deref() Nature {
	if n.Type != nil {
		n.Type = deref.Type(n.Type)
	}
	return n
}

func (n Nature) Kind() reflect.Kind {
	if n.Type != nil {
		return n.Type.Kind()
	}
	return reflect.Invalid
}

func (n Nature) Key() Nature {
	if n.Kind() == reflect.Map {
		return Nature{Type: n.Type.Key()}
	}
	return unknown
}

func (n Nature) Elem() Nature {
	switch n.Kind() {
	case reflect.Ptr:
		return Nature{Type: n.Type.Elem()}
	case reflect.Map:
		if n.DefaultMapValue != nil {
			return *n.DefaultMapValue
		}
		return Nature{Type: n.Type.Elem()}
	case reflect.Array, reflect.Slice:
		if n.ArrayOf != nil {
			return *n.ArrayOf
		}
		return Nature{Type: n.Type.Elem()}
	}
	return unknown
}

func (n Nature) AssignableTo(nt Nature) bool {
	if n.Nil {
		// Untyped nil is assignable to any interface, but implements only the empty interface.
		if nt.IsAny() {
			return true
		}
	}
	if n.Type == nil || nt.Type == nil {
		return false
	}
	return n.Type.AssignableTo(nt.Type)
}

func (n Nature) NumMethods() int {
	if n.Type == nil {
		return 0
	}
	return n.Type.NumMethod()
}

func (n Nature) MethodByName(name string) (Nature, bool) {
	if n.Type == nil {
		return unknown, false
	}
	method, ok := n.Type.MethodByName(name)
	if !ok {
		return unknown, false
	}

	if n.Type.Kind() == reflect.Interface {
		// In case of interface type method will not have a receiver,
		// and to prevent checker decreasing numbers of in arguments
		// return method type as not method (second argument is false).

		// Also, we can not use m.Index here, because it will be
		// different indexes for different types which implement
		// the same interface.
		return Nature{Type: method.Type}, true
	} else {
		return Nature{
			Type:        method.Type,
			Method:      true,
			MethodIndex: method.Index,
		}, true
	}
}

func (n Nature) NumIn() int {
	if n.Type == nil {
		return 0
	}
	return n.Type.NumIn()
}

func (n Nature) In(i int) Nature {
	if n.Type == nil {
		return unknown
	}
	return Nature{Type: n.Type.In(i)}
}

func (n Nature) NumOut() int {
	if n.Type == nil {
		return 0
	}
	return n.Type.NumOut()
}

func (n Nature) Out(i int) Nature {
	if n.Type == nil {
		return unknown
	}
	return Nature{Type: n.Type.Out(i)}
}

func (n Nature) IsVariadic() bool {
	if n.Type == nil {
		return false
	}
	return n.Type.IsVariadic()
}

func (n Nature) FieldByName(name string) (Nature, bool) {
	if n.Type == nil {
		return unknown, false
	}
	field, ok := fetchField(n.Type, name)
	return Nature{Type: field.Type, FieldIndex: field.Index}, ok
}

func (n Nature) PkgPath() string {
	if n.Type == nil {
		return ""
	}
	return n.Type.PkgPath()
}

func (n Nature) IsFastMap() bool {
	if n.Type == nil {
		return false
	}
	if n.Type.Kind() == reflect.Map &&
		n.Type.Key().Kind() == reflect.String &&
		n.Type.Elem().Kind() == reflect.Interface {
		return true
	}
	return false
}

func (n Nature) Get(name string) (Nature, bool) {
	if n.Type == nil {
		return unknown, false
	}

	if m, ok := n.MethodByName(name); ok {
		return m, true
	}

	t := deref.Type(n.Type)

	switch t.Kind() {
	case reflect.Struct:
		if f, ok := fetchField(t, name); ok {
			return Nature{
				Type:       f.Type,
				FieldIndex: f.Index,
			}, true
		}
	case reflect.Map:
		if f, ok := n.Fields[name]; ok {
			return f, true
		}
	}
	return unknown, false
}

func (n Nature) All() map[string]Nature {
	table := make(map[string]Nature)

	if n.Type == nil {
		return table
	}

	for i := 0; i < n.Type.NumMethod(); i++ {
		method := n.Type.Method(i)
		table[method.Name] = Nature{
			Type:        method.Type,
			Method:      true,
			MethodIndex: method.Index,
		}
	}

	t := deref.Type(n.Type)

	switch t.Kind() {
	case reflect.Struct:
		for name, nt := range StructFields(t) {
			if _, ok := table[name]; ok {
				continue
			}
			table[name] = nt
		}

	case reflect.Map:
		for key, nt := range n.Fields {
			if _, ok := table[key]; ok {
				continue
			}
			table[key] = nt
		}
	}

	return table
}
