package checker

import (
	"reflect"

	"github.com/expr-lang/expr/ast"
	. "github.com/expr-lang/expr/checker/nature"
	"github.com/expr-lang/expr/vm"
)

func FieldIndex(env Nature, node ast.Node) (bool, []int, string) {
	switch n := node.(type) {
	case *ast.IdentifierNode:
		if env.Kind() == reflect.Struct {
			if field, ok := env.Get(n.Value); ok && len(field.FieldIndex) > 0 {
				return true, field.FieldIndex, n.Value
			}
		}
	case *ast.MemberNode:
		base := n.Node.Nature()
		base = base.Deref()
		if base.Kind() == reflect.Struct {
			if prop, ok := n.Property.(*ast.StringNode); ok {
				name := prop.Value
				if field, ok := base.FieldByName(name); ok {
					return true, field.FieldIndex, name
				}
			}
		}
	}
	return false, nil, ""
}

// MethodIndex 判断某个 AST 节点是否是一个方法调用，并返回其是否是方法、方法索引、方法名。
//
// 背景：
//
//	在 Go reflect 中，如果你有一个结构体 T，你可以通过 T.Method(i) 获得它的第 i 个方法。
//	reflect.Method 中的 .Index 字段告诉你这个方法在 Type.Method(i) 中的位置，也常用于 v.Method(index).Call(...) 反射调用。
//
// 示例：
//
//	(1)

//	type Env struct {
//		Print func()
//	}
//
//	env := Nature{
//		Value: reflect.ValueOf(Env{})
//	}
//	node := &ast.IdentifierNode{
//		Value: "Print"
//	}
//
//	found, index, name := MethodIndex(env, node) // (true, 0, "Print")
//
// (2)
//
//	type User struct{}
//	func (u User) GetName() string {
//		return "Alice"
//	}
//
//	node := &ast.MemberNode{
//		Node:     &ast.IdentifierNode{Value: "user"}, 	// `user`
//		Property: &ast.StringNode{Value: "GetName"}, 	// `.GetName`
//	}
//
//	found, index, name := MethodIndex(env, node) 		// (true, 0, "GetName")
//
// (3)
//
//	node := &ast.IdentifierNode{
//		Value: "NonExistentMethod"
//	}
//	found, index, name := MethodIndex(env, node) 		// (false, 0, "")
func MethodIndex(env Nature, node ast.Node) (bool, int, string) {
	switch n := node.(type) {
	case *ast.IdentifierNode:
		if env.Kind() == reflect.Struct {
			if m, ok := env.Get(n.Value); ok {
				return m.Method, m.MethodIndex, n.Value
			}
		}
	case *ast.MemberNode:
		if name, ok := n.Property.(*ast.StringNode); ok {
			base := n.Node.Type()
			if base != nil && base.Kind() != reflect.Interface {
				if m, ok := base.MethodByName(name.Value); ok {
					return true, m.Index, name.Value
				}
			}
		}
	}
	return false, 0, ""
}

// TypedFuncIndex
//
// 检查一个函数类型 fn 是否符合预定义的某种函数签名，并返回匹配的索引（如果找到）。
// 某些 VM 或解释器（如表达式引擎）需要高效调用 Go 函数，直接匹配预定义的函数类型可以避免反射开销。
// 检查一个函数类型 (reflect.Type) 是否符合预定义的某种函数签名模式，并返回匹配的索引（如果找到）。
//
// 输入：
//   - fn：要检查的函数类型（reflect.Type）。
//   - method：是否是方法（true 表示方法，false 表示普通函数）。
//
// 输出：(int, bool)
//   - 如果匹配成功，返回 (索引, true)。
//   - 如果匹配失败，返回 (0, false)。
//
// 检查逻辑：
//   - fn 不能是 nil
//   - 必须是函数类型
//   - 不能是可变参数函数
//   - 不能是命名函数类型
//
// 遍历预定义的函数类型 (vm.FuncTypes)
//   - 跳过 i=0（可能是占位符或无效类型）
//   - 检查返回值数量是否匹配
//   - 检查每个返回值类型是否匹配
//   - 检查参数数量是否匹配
//   - 检查每个参数类型是否匹配（如果是方法，跳过 receiver）
//   - 匹配成功，返回当前索引 i 和 true
//
// 适用场景
//   - 虚拟机（VM）或解释器： 预定义一组支持的函数签名（vm.FuncTypes），运行时检查用户提供的函数是否符合其中一种签名，以便高效调用。
//   - RPC 或插件系统：确保注册的函数符合某种调用规范。
//   - 代码生成工具：生成适配器代码，将不同签名的函数转换为标准形式。
//
// 示例
//
//	假设 vm.FuncTypes 包含以下预定义类型：
//
//	var FuncTypes = []interface{}{
//		nil, 							// i=0，占位符
//		func(int) int{},               	// i=1
//		func(string, int) (int, error) 	// i=2
//	}
//
//	案例 1：匹配普通函数
//
//	func MyFunc(a int) int { return a }
//
//	fnType := reflect.TypeOf(MyFunc)
//	index, ok := TypedFuncIndex(fnType, false) // index = 1, ok = true
//
//	案例 2：匹配方法
//
//	type MyStruct struct{}
//	func (m *MyStruct) MyMethod(s string, n int) (int, error) { return n, nil }
//
//	fnType := reflect.TypeOf((*MyStruct).MyMethod)
//	index, ok := TypedFuncIndex(fnType, true) // index = 2, ok = true（忽略 receiver）
//
//	案例 3：不匹配的情况
//
//	func WrongFunc(a float64) int { return 0 }
//
//	fnType := reflect.TypeOf(WrongFunc)
//	index, ok := TypedFuncIndex(fnType, false) // index = 0, ok = false（参数类型不匹配）

// TypedFuncIndex 通过编译期类型匹配将动态调用转换为静态调用，省去反射类型检查，显著提升性能。
// 它判断 fn 是否与 vm.FuncTypes 中某个函数类型完全一致（输入参数、返回值都一样），是则返回其索引。
//
// 主要作用：
//   - 检查函数类型是否适合使用特化调用指令（OpCallTyped）
//   - 返回匹配的预定义函数模板索引（用于生成高效调用指令）
//
// 特殊情况：
//   - 变参函数，参数展开逻辑复杂，无法提前注册匹配，难以静态优化
//   - 命名函数类型如 type MyFunc func(int) int 不支持
func TypedFuncIndex(fn reflect.Type, method bool) (int, bool) {
	if fn == nil {
		return 0, false
	}
	if fn.Kind() != reflect.Func {
		return 0, false
	}
	// OnCallTyped doesn't work for functions with variadic arguments.
	if fn.IsVariadic() {
		return 0, false
	}
	// OnCallTyped doesn't work named function, like `type MyFunc func() int`.
	if fn.PkgPath() != "" { // If PkgPath() is not empty, it means that function is named.
		return 0, false
	}

	fnNumIn := fn.NumIn()
	fnInOffset := 0
	if method {
		fnNumIn--
		fnInOffset = 1
	}

funcTypes:
	for i := range vm.FuncTypes {
		if i == 0 {
			continue
		}
		typed := reflect.ValueOf(vm.FuncTypes[i]).Elem().Type()
		if typed.Kind() != reflect.Func {
			continue
		}
		// 返回值数量和类型完全一致
		if typed.NumOut() != fn.NumOut() {
			continue
		}
		for j := 0; j < typed.NumOut(); j++ {
			if typed.Out(j) != fn.Out(j) {
				continue funcTypes
			}
		}
		// 参数数量和类型完全一致
		if typed.NumIn() != fnNumIn {
			continue
		}
		for j := 0; j < typed.NumIn(); j++ {
			if typed.In(j) != fn.In(j+fnInOffset) {
				continue funcTypes
			}
		}
		return i, true
	}
	return 0, false
}

// IsFastFunc 检查 fn 是否符合某种特定的函数签名：
//   - 类型：		reflect.Func
//   - 可变参数：		IsVariadic() == true
//   - 参数数量：		普通函数 1 个，方法 2 个
//   - 返回值数量：	1 个
//   - 返回值类型：	interface{}
//   - 可变参数类型：	...interface{}（即 []interface{}）
//
// 即：
//   - func(receiverType, args ...interface{}) interface{}
//   - func(args ...interface{}) interface{}
//
// 例如：
//
//	func Println(args ...interface{}) interface{} {
//		fmt.Println(args...)
//		return nil
//	}
//
//	func (logger Logger) Log(args ...interface{}) interface{} {
//		// ...
//	}
//
// 场景：通常用于动态调用函数的场景，如：
//   - RPC 框架：判断某个函数是否符合 RPC 调用规范（如接收 ...interface{} 并返回 interface{}）。
//   - 脚本引擎：检查函数是否可以被脚本直接调用。
//   - 依赖注入：识别符合特定签名的函数，用于自动绑定参数。
//
// 示例：
//
//	 (1)
//		func MyFunc(args ...interface{}) interface{} {
//		   return args[0]
//		}
//		fnType := reflect.TypeOf(MyFunc)
//		fmt.Println(IsFastFunc(fnType, false))  // true
//	 (2)
//		type MyStruct struct{}
//		func (m *MyStruct) MyMethod(args ...interface{}) interface{} {
//		   	return args[0]
//		}
//		fnType := reflect.TypeOf((*MyStruct).MyMethod)
//		fmt.Println(IsFastFunc(fnType, true))  // true
//
// 反例：
//
//	func bad(args ...interface{}) (interface{}, error)  // 多返回值
//	func bad(args ...int) interface{}                  	// 参数类型不是 interface{}
//	func bad(a int, b string) interface{}             	// 非 variadic
func IsFastFunc(fn reflect.Type, method bool) bool {
	if fn == nil {
		return false
	}
	if fn.Kind() != reflect.Func {
		return false
	}

	numIn := 1
	if method {
		numIn = 2
	}

	if fn.IsVariadic() &&
		fn.NumIn() == numIn &&
		fn.NumOut() == 1 &&
		fn.Out(0).Kind() == reflect.Interface {
		rest := fn.In(fn.NumIn() - 1) // function has only one param for functions and two for methods
		if kind(rest) == reflect.Slice && rest.Elem().Kind() == reflect.Interface {
			return true
		}
	}
	return false
}
