package conf

import (
	"fmt"
	"reflect"

	. "github.com/expr-lang/expr/checker/nature"
	"github.com/expr-lang/expr/internal/deref"
	"github.com/expr-lang/expr/types"
)

// Env 将任意类型的 env 转换为对应的 Nature 类型描述。
//
// 转换逻辑：
//
//	nil → 空 map
//	types.Map → 直接转换
//	struct → 记录类型，延迟字段解析（用 All()）
//	map → 遍历 key/value，把每个 value 转成 Nature
//	其它类型 → panic
//
// 示例：
//  1. nil
//     env := nil
//     nature := Env(env)
//  返回:
// 	{
// 		Type: map[string]interface{},
//		Strict: true,
//		Fields: {},
//	}
//
//  2. struct
//
//	type Config struct {
//	   Timeout int
//	   Enabled bool
//	}
//	env := Config{ Timeout: 30, Enabled: true }
//	nature := Env(env)
//
//	返回:
//	{
//		Type: Config,
//		Strict: true,
//	}
//
//  3. map
//
//  env := map[string]interface{}{
//    "timeout": 30,
//    "enabled": true,
//    "user":    User{Name: "John"},
//  }
//  nature := Env(env)
//
//  返回：
//  {
// 		Type: map[string]interface{},
//		Strict: true,
//		Fields: {
//		  "timeout": {Type: int},
//		  "enabled": {Type: bool},
//		  "user":    {Type: User}
//		},
//	}

func Env(env any) Nature {
	if env == nil {
		return Nature{
			Type:   reflect.TypeOf(map[string]any{}),
			Strict: true,
		}
	}

	switch env := env.(type) {
	case types.Map:
		return env.Nature()
	}

	v := reflect.ValueOf(env)
	d := deref.Value(v)

	switch d.Kind() {
	case reflect.Struct:
		return Nature{
			Type:   v.Type(),
			Strict: true,
		}

	case reflect.Map:
		n := Nature{
			Type:   v.Type(),
			Fields: make(map[string]Nature, v.Len()),
			Strict: true,
		}

		for _, key := range v.MapKeys() {
			elem := v.MapIndex(key)
			if !elem.IsValid() || !elem.CanInterface() {
				panic(fmt.Sprintf("invalid map value: %s", key))
			}

			face := elem.Interface()
			switch face := face.(type) {
			case types.Map:
				n.Fields[key.String()] = face.Nature()
			default:
				if face == nil {
					n.Fields[key.String()] = Nature{Nil: true}
					continue
				}
				n.Fields[key.String()] = Nature{Type: reflect.TypeOf(face)}
			}

		}

		return n
	}

	panic(fmt.Sprintf("unknown type %T", env))
}
