package nature

import (
	"reflect"

	"github.com/expr-lang/expr/internal/deref"
)

// 如果字段有 expr 标签，则直接使用该标签作为字段名；否则使用字段本身的名字。
//
// 示例：
//
//	type User struct {
//		Name string `expr:"username"`
//		Age  int
//	}
//
// fieldName(Name) → "username"
// fieldName(Age)  → "Age"
func fieldName(field reflect.StructField) string {
	if taggedName := field.Tag.Get("expr"); taggedName != "" {
		return taggedName
	}
	return field.Name
}

// 从结构体类型 t 中查找名为 name 的字段
func fetchField(t reflect.Type, name string) (reflect.StructField, bool) {
	// If t is not a struct, early return.
	if t.Kind() != reflect.Struct {
		return reflect.StructField{}, false
	}

	// First check all structs fields.
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		// Search all fields, even embedded structs.
		if fieldName(field) == name {
			return field, true
		}
	}

	// Second check fields of embedded structs.
	for i := 0; i < t.NumField(); i++ {
		anon := t.Field(i)
		if anon.Anonymous {
			anonType := anon.Type
			if anonType.Kind() == reflect.Pointer {
				anonType = anonType.Elem()
			}
			if field, ok := fetchField(anonType, name); ok {
				field.Index = append(anon.Index, field.Index...)
				return field, true
			}
		}
	}

	return reflect.StructField{}, false
}

// StructFields 从结构体类型 reflect.Type 中提取字段信息，包括：
//   - 支持结构体 tag（通过 expr 标签指定字段名）；
//   - 支持匿名嵌套字段（递归解析嵌入的 struct）；
func StructFields(t reflect.Type) map[string]Nature {
	table := make(map[string]Nature)

	t = deref.Type(t)
	if t == nil {
		return table
	}

	switch t.Kind() {
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)

			if f.Anonymous {
				for name, typ := range StructFields(f.Type) {
					if _, ok := table[name]; ok {
						continue
					}

					// type Inner struct {
					//     Y int
					// }
					//
					// type Outer struct {
					//     Inner
					// }
					//
					// f.Index 是匿名嵌套字段（如 Inner）在外层结构体（如 Outer）中的索引（[0]）。
					// typ.FieldIndex 是嵌套结构体内部字段（如 Y）的索引（[0]）。
					typ.FieldIndex = append(f.Index, typ.FieldIndex...)
					table[name] = typ
				}
			}

			table[fieldName(f)] = Nature{
				Type:       f.Type,
				FieldIndex: f.Index,
			}
		}
	}

	return table
}
