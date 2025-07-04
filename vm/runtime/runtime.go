package runtime

//go:generate sh -c "go run ./helpers > ./helpers[generated].go"

import (
	"fmt"
	"math"
	"reflect"

	"github.com/expr-lang/expr/internal/deref"
)

// Fetch 从各种数据结构中提取元素或字段，支持以下数据类型：
//   - 数组/切片/字符串（通过索引访问）
//   - 映射（通过 key 访问）
//   - 结构体（通过字段名访问）
//   - 方法（通过方法名调用）
func Fetch(from, i any) any {
	v := reflect.ValueOf(from)
	if v.Kind() == reflect.Invalid {
		panic(fmt.Sprintf("cannot fetch %v from %T", i, from))
	}

	// Methods can be defined on any type.
	if v.NumMethod() > 0 {
		if methodName, ok := i.(string); ok {
			method := v.MethodByName(methodName)
			if method.IsValid() {
				return method.Interface()
			}
		}
	}

	// Structs, maps, and slices can be access through a pointer or through
	// a value, when they are accessed through a pointer we don't want to
	// copy them to a value.
	// De-reference everything if necessary (interface and pointers)
	v = deref.Value(v)

	switch v.Kind() {
	case reflect.Array, reflect.Slice, reflect.String:
		index := ToInt(i)
		l := v.Len()
		if index < 0 {
			index = l + index
		}
		if index < 0 || index >= l {
			panic(fmt.Sprintf("index out of range: %v (array length is %v)", index, l))
		}
		value := v.Index(index)
		if value.IsValid() {
			return value.Interface()
		}

	case reflect.Map:
		var value reflect.Value
		if i == nil {
			value = v.MapIndex(reflect.Zero(v.Type().Key()))
		} else {
			value = v.MapIndex(reflect.ValueOf(i))
		}
		if value.IsValid() {
			return value.Interface()
		} else {
			elem := reflect.TypeOf(from).Elem()
			return reflect.Zero(elem).Interface()
		}

	case reflect.Struct:
		fieldName := i.(string)
		value := v.FieldByNameFunc(func(name string) bool {
			field, _ := v.Type().FieldByName(name)
			if field.Tag.Get("expr") == fieldName {
				return true
			}
			return name == fieldName
		})
		if value.IsValid() {
			return value.Interface()
		}
	}
	panic(fmt.Sprintf("cannot fetch %v from %T", i, from))
}

type Field struct {
	Index []int
	Path  []string
}

// FetchField 从结构体中获取嵌套字段的值，支持通过索引路径访问嵌套结构体字段。
//
// 示例：
//
//	user := User{
//	   Name: "John",
//	   Address: &struct{City string; Zip int}{
//	       City: "NY",
//	       Zip: 10001,
//	   },
//	}
//
//	field := &Field{
//	   Index: []int{1, 0}, // Address -> City
//	   Path: []string{"Address", "City"},
//	}
//
//	FetchField(user, field) // 返回 "NY"
func FetchField(from any, field *Field) any {
	v := reflect.ValueOf(from)
	if v.Kind() != reflect.Invalid {
		v = reflect.Indirect(v)

		// We can use v.FieldByIndex here, but it will panic if the field
		// is not exists. And we need to recover() to generate a more
		// user-friendly error message.
		// Also, our fieldByIndex() function is slightly faster than the
		// v.FieldByIndex() function as we don't need to verify what a field
		// is a struct as we already did it on compilation step.
		value := fieldByIndex(v, field)
		if value.IsValid() {
			return value.Interface()
		}
	}
	panic(fmt.Sprintf("cannot get %v from %T", field.Path[0], from))
}

func fieldByIndex(v reflect.Value, field *Field) reflect.Value {
	if len(field.Index) == 1 {
		return v.Field(field.Index[0])
	}
	for i, x := range field.Index {
		if i > 0 {
			if v.Kind() == reflect.Ptr {
				if v.IsNil() {
					panic(fmt.Sprintf("cannot get %v from %v", field.Path[i], field.Path[i-1]))
				}
				v = v.Elem()
			}
		}
		v = v.Field(x)
	}
	return v
}

type Method struct {
	Index int
	Name  string
}

func FetchMethod(from any, method *Method) any {
	v := reflect.ValueOf(from)
	kind := v.Kind()
	if kind != reflect.Invalid {
		// Methods can be defined on any type, no need to dereference.
		method := v.Method(method.Index)
		if method.IsValid() {
			return method.Interface()
		}
	}
	panic(fmt.Sprintf("cannot fetch %v from %T", method.Name, from))
}

// Slice
//
// 示例
//
//	// 数组切片
//	arr := [5]int{1, 2, 3, 4, 5}
//	Slice(arr, 1, 3)  // 返回 [2, 3]
//
//	// 切片操作
//	Slice([]string{"a", "b", "c"}, 0, -1)  // 返回 ["a", "b"]
//
//	// 字符串切片
//	Slice("hello", 1, 4)  // 返回 "ell"
//
//	// 负索引
//	Slice([]int{1, 2, 3, 4}, -3, -1)  // 返回 [2, 3]
//
//	// 指针解引用
//	str := "golang"
//	Slice(&str, 2, 4)  // 返回 "la"
func Slice(array, from, to any) any {
	v := reflect.ValueOf(array)
	switch v.Kind() {
	case reflect.Array, reflect.Slice, reflect.String:
		length := v.Len()
		a, b := ToInt(from), ToInt(to)
		if a < 0 {
			a = length + a
		}
		if a < 0 {
			a = 0
		}
		if b < 0 {
			b = length + b
		}
		if b < 0 {
			b = 0
		}
		if b > length {
			b = length
		}
		if a > b {
			a = b
		}
		value := v.Slice(a, b)
		if value.IsValid() {
			return value.Interface()
		}

	case reflect.Ptr:
		value := v.Elem()
		if value.IsValid() {
			return Slice(value.Interface(), from, to)
		}

	}
	panic(fmt.Sprintf("cannot slice %v", from))
}

// In
//
// 示例
//   - In(3, []int{1, 2, 3})           		// true
//   - In("a", map[string]int{"a": 1})  	// true
//   - In("Name", struct{Name string}{}) 	// true
//   - In(4, []int{1, 2, 3})           		// false
//   - In(nil, []int{1, 2, 3})        	 	// false
func In(needle any, array any) bool {
	if array == nil {
		return false
	}
	v := reflect.ValueOf(array)

	switch v.Kind() {
	case reflect.Array, reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			value := v.Index(i)
			if value.IsValid() {
				if Equal(value.Interface(), needle) {
					return true
				}
			}
		}
		return false
	case reflect.Map:
		var value reflect.Value
		if needle == nil {
			value = v.MapIndex(reflect.Zero(v.Type().Key()))
		} else {
			value = v.MapIndex(reflect.ValueOf(needle))
		}
		if value.IsValid() {
			return true
		}
		return false
	case reflect.Struct:
		n := reflect.ValueOf(needle)
		if !n.IsValid() || n.Kind() != reflect.String {
			panic(fmt.Sprintf("cannot use %T as field name of %T", needle, array))
		}
		value := v.FieldByName(n.String())
		if value.IsValid() {
			return true
		}
		return false
	case reflect.Ptr:
		value := v.Elem()
		if value.IsValid() {
			return In(needle, value.Interface())
		}
		return false
	}
	panic(fmt.Sprintf(`operator "in" not defined on %T`, array))
}

func Len(a any) int {
	v := reflect.ValueOf(a)
	switch v.Kind() {
	case reflect.Array, reflect.Slice, reflect.Map, reflect.String:
		return v.Len()
	default:
		panic(fmt.Sprintf("invalid argument for len (type %T)", a))
	}
}

func Negate(i any) any {
	switch v := i.(type) {
	case float32:
		return -v
	case float64:
		return -v
	case int:
		return -v
	case int8:
		return -v
	case int16:
		return -v
	case int32:
		return -v
	case int64:
		return -v
	case uint:
		return -v
	case uint8:
		return -v
	case uint16:
		return -v
	case uint32:
		return -v
	case uint64:
		return -v
	default:
		panic(fmt.Sprintf("invalid operation: - %T", v))
	}
}

func Exponent(a, b any) float64 {
	return math.Pow(ToFloat64(a), ToFloat64(b))
}

// MakeRange 创建一个 []int 切片，包含从 min 到 max 的所有整数。
//
// 例子:
//   - MakeRange(1, 5)  // 返回 [1, 2, 3, 4, 5]
//   - MakeRange(3, 3)  // 返回 [3]
//   - MakeRange(5, 2)  // 返回 []
//   - MakeRange(-2, 2) // 返回 [-2, -1, 0, 1, 2]
func MakeRange(min, max int) []int {
	size := max - min + 1
	if size <= 0 {
		return []int{}
	}
	rng := make([]int, size)
	for i := range rng {
		rng[i] = min + i
	}
	return rng
}

func ToInt(a any) int {
	switch x := a.(type) {
	case float32:
		return int(x)
	case float64:
		return int(x)
	case int:
		return x
	case int8:
		return int(x)
	case int16:
		return int(x)
	case int32:
		return int(x)
	case int64:
		return int(x)
	case uint:
		return int(x)
	case uint8:
		return int(x)
	case uint16:
		return int(x)
	case uint32:
		return int(x)
	case uint64:
		return int(x)
	default:
		panic(fmt.Sprintf("invalid operation: int(%T)", x))
	}
}

func ToInt64(a any) int64 {
	switch x := a.(type) {
	case float32:
		return int64(x)
	case float64:
		return int64(x)
	case int:
		return int64(x)
	case int8:
		return int64(x)
	case int16:
		return int64(x)
	case int32:
		return int64(x)
	case int64:
		return x
	case uint:
		return int64(x)
	case uint8:
		return int64(x)
	case uint16:
		return int64(x)
	case uint32:
		return int64(x)
	case uint64:
		return int64(x)
	default:
		panic(fmt.Sprintf("invalid operation: int64(%T)", x))
	}
}

func ToFloat64(a any) float64 {
	switch x := a.(type) {
	case float32:
		return float64(x)
	case float64:
		return x
	case int:
		return float64(x)
	case int8:
		return float64(x)
	case int16:
		return float64(x)
	case int32:
		return float64(x)
	case int64:
		return float64(x)
	case uint:
		return float64(x)
	case uint8:
		return float64(x)
	case uint16:
		return float64(x)
	case uint32:
		return float64(x)
	case uint64:
		return float64(x)
	default:
		panic(fmt.Sprintf("invalid operation: float(%T)", x))
	}
}

func IsNil(v any) bool {
	if v == nil {
		return true
	}
	r := reflect.ValueOf(v)
	switch r.Kind() {
	case reflect.Chan, reflect.Func, reflect.Map, reflect.Ptr, reflect.Interface, reflect.Slice:
		return r.IsNil()
	default:
		return false
	}
}
