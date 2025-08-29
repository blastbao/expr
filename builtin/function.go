package builtin

import (
	"reflect"
)

type Function struct {
	Name      string                                          // 函数名
	Fast      func(arg any) any                               // 快速调用版本，假设函数只有一个参数一个返回值，没有错误返回，性能最优。
	Func      func(args ...any) (any, error)                  // 标准调用版本，支持可变参数，返回 (any, error)
	Safe      func(args ...any) (any, uint, error)            // 安全调用版本，多返回一个 uint（可能代表错误码或执行状态），适合需要额外元信息的场景。
	Types     []reflect.Type                                  // 类型签名列表，支持函数重载和类型检查，存储备选函数的输入输出类型，比如 func(int, string) bool，就会存成 [int, string, bool]。
	Validate  func(args []reflect.Type) (reflect.Type, error) // 自定义验证器，用来验证参数类型是否匹配、返回值类型是否正确；输入是参数类型列表，返回函数的返回类型或错误。
	Deref     func(i int, arg reflect.Type) bool              // 解引用控制，指定哪些参数需要自动解引用；参数 i 是参数索引，arg 是参数类型，返回 true 表示该参数需要解引用。
	Predicate bool                                            // 标记该函数是否为谓词函数（返回布尔值），常用于过滤/条件判断。
}

func (f *Function) Type() reflect.Type {
	if len(f.Types) > 0 {
		return f.Types[0] // 返回第一个类型签名
	}
	return reflect.TypeOf(f.Func) // 返回函数本身的类型（反射）
}

// 使用示例
//
//
//	示例一、注册一个乘法函数
//
//	f := &Function{
//    Name: "Multiply",
//    Func: func(args ...any) (any, error) {
//        a := args[0].(int)
//        b := args[1].(int)
//        return a * b, nil
//    },
//    Types: types(func(int, int) int), // 用 types() 辅助函数生成
//	}
//
//	Types[0] 是 (int,int) → int
//	f.Type() 会返回 reflect.Type ，等价于 func(int,int) int
//
//	调用：
//	res, _ := f.Func(2, 4)
//	fmt.Println(res) // 8
//
//  示例二、带 Validate 和 Predicate 的函数
//
//	f := &Function{
//	   Name: "IsString",
//	   Func: func(args ...any) (any, error) {
//		   _, ok := args[0].(string)
//		   return ok, nil
//	   },
//	   Types: types(func(any) bool),
//	   Validate: func(args []reflect.Type) (reflect.Type, error) {
//		   if len(args) != 1 {
//			   return nil, fmt.Errorf("IsString expects 1 argument")
//		   }
//		   return reflect.TypeOf(true), nil
//	   },
//	   Predicate: true, // 表示这是一个逻辑谓词函数
//	}

// Q: 为什么 Func 和 Types 字段不匹配
//
// Func 和 Types，其实是两个不同层面的东西：
//
//	1. Func: func(args ...any) (any, error)
//
//	这是运行时的真实执行函数。
//	所有函数都用一个统一的签名来注册：func(args ...any) (any, error)。
//	这样无论 IsString、Add、Substring，都能存在同一个函数表里，调用时只要传 []any 参数就行。
//
//	例如：IsString("foo") → 在运行时执行 args[0].(string)，返回 true, nil。
//
//	2. Types: types(func(any) bool)
//
//	这是类型系统 / 类型检查用的声明，不是实际执行函数。
//	它声明了这个函数的签名：
//	输入：any
//	输出：bool
//	用来在 AST → 类型检查时，推导出表达式的返回类型。
//
//	例如：expr := IsString(x) 在做类型检查时，根据 Types 推导出 expr 的返回类型是 bool。
//	这样后续逻辑可以知道 if IsString(x) { ... } 是合法的。
//
//	3. 为什么看起来“不匹配”？
//
//	因为 一个是 runtime 层面统一调用约定，一个是 compile/typecheck 层面的签名描述。
//	 - Func → 真正执行，参数收敛成 []any，返回 (any, error)。
//	 - Types → 告诉类型检查器“逻辑上的函数签名”，比如 func(any) bool。
//
//	4. 举个对比例子
//
//	假如你实现 Add 函数：
//	f := &Function{
//	   Name: "Add",
//	   Func: func(args ...any) (any, error) {
//		   return args[0].(int) + args[1].(int), nil
//	   },
//	   Types: types(func(int, int) int),
//	}
//	运行时 → 全部走 func(args ...any) (any, error)，没问题。
//	类型检查时 → 你可以得到“Add 的签名是 func(int, int) int”，所以 Add(1, "foo") 会在编译阶段报错。
//
//	所以你看到的“不匹配”，其实是 “两个不同维度的描述”：
//	 - runtime 执行 → 统一签名 (args ...any) (any, error)。
//	 - compile/typecheck → 实际逻辑签名 func(any) bool。

// Q: Deref 有什么用？
//
//	假设我们有一个 IsPositive 函数，逻辑是「判断一个整数是否大于 0」。
//	我们希望这个函数既能接收 int 类型（值），也能接收 *int 类型（指针）—— 但如果没有 Deref，函数的类型检查会出问题：
//
//	如果 Types 定义为 `func(int) bool` ，当用户传 *int 类型参数时，引擎会认为「类型不匹配」，直接报错；
//	如果 Types 定义为 `func(*int) bool` ，用户传 int 时又会报错。
//
//	这时候 Deref 就派上用场了：它可以告诉引擎「对某个参数，先解引用再做类型检查」，这样无论用户传值还是指针，都能通过检查。
//
// 举例说明
//
//	假设有一个函数：
//
//	f := &Function{
//	   Name: "Length",
//	   Func: func(args ...any) (any, error) {
//		   s := args[0].(string)
//		   return len(s), nil
//	   },
//	   Types: types(func(string) int),
//	   Deref: func(i int, arg reflect.Type) bool {
//		   // 如果传入的是 *string，则解引用
//		   return arg.Kind() == reflect.Ptr && arg.Elem().Kind() == reflect.String
//	   },
//	}
//
//	如果你传入 "hello"，Deref 返回 false，直接用值。
//	如果你传入 new("hello")，Deref 返回 true，就会先取 *ptr 再调用 Func。
//
//  这样函数不用关心用户传的是值还是指针，通过解引用统一处理；避免在 Func 里写大量「判断参数是否为指针」的重复代码。
//
// 如果不用 Deref ，可以用函数重载 + Func 内判断的方式来实现。
//
// f := &Function{
//    Name: "IsString",
//    Func: func(args ...any) (any, error) {
//        switch v := args[0].(type) {
//        case string:
//            return true, nil
//        case *string:
//            return v != nil, nil
//        default:
//            return false, fmt.Errorf("invalid type %T, expected string or *string", v)
//        }
//    },
//    Types: types(
//        func(string) bool,  // 签名1：string → bool
//        func(*string) bool, // 签名2：*string → bool
//    ),
//    Predicate: true,
// }
//
// 函数对外支持多种调用方式，表达式引擎会根据用户传入的实际参数类型，匹配 Types 中对应的类型签名。
// 实际执行时，Func 通过 switch + 类型断言，根据参数的真实类型执行对应的逻辑，实现了 “同一个函数名，不同参数类型做不同处理” 的重载效果。
//
// 使用 Deref 比较轻量，重载比较重。

// Q: 为什么需要 Fast？
//
// Fast 设计的目的是：在参数数量固定且类型明确、几乎不会出错的场景下，跳过参数解析、错误处理等通用逻辑，直接执行核心计算，从而减少函数调用的性能开销。
// Fast 和 Func 的定位完全不同：
//	- Func 是通用、安全的调用入口：需要处理可变参数、错误检查、类型校验等各种边缘情况，因此必须设计得更 “重”（带 ...any 和 error），保证通用性和安全性。
//	- Fast 是专用、高效的调用入口：只针对 “参数固定为 1 个、且调用时能确保参数类型正确、几乎不会出错” 的场景（比如框架内部的高频调用），因此可以简化原型，省去错误处理和可变参数的额外开销。
//
// 虽然统一的函数原型是 func(args ...any) (any, error)，但这有几个性能问题：
//	- 可变参数开销：...any 需要创建切片
//	- 类型断言开销：需要从 any 转换回具体类型
//	- 错误处理开销：即使不会出错也要返回 error
// Fast 目的是提供了一个零开销的快速路径：
//	- 单参数：只处理一个参数，避免可变参数开销
//	- 无错误返回：假设不会出错，避免错误处理开销
//	- 直接返回：返回具体类型，减少类型断言
//
// 工作原理
//
//	表达式引擎会优先使用 Fast（如果存在）：
//
//	func callFunction(f *Function, args []any) (any, error) {
//	   // 优先使用 Fast 路径
//	   if f.Fast != nil && len(args) == 1 {
//		   return f.Fast(args[0]), nil
//	   }
//
//	   // 退回到标准路径
//	   if f.Func != nil {
//		   return f.Func(args...)
//	   }
//
//	   return nil, errors.New("function not implemented")
//	}
//
// 示例
//
//	假设我们有一个 ToUpper 函数，功能是将字符串转为大写：
//
//	f := &Function{
//	   Name: "ToUpper",
//	   Func: func(args ...any) (any, error) { // 通用版本：支持可变参数检查、错误处理
//		   if len(args) != 1 {
//			   return "", fmt.Errorf("需要1个参数")
//		   }
//		   s, ok := args[0].(string)
//		   if !ok {
//			   return "", fmt.Errorf("参数必须是字符串")
//		   }
//		   return strings.ToUpper(s), nil
//	   },
//	   Fast: func(arg any) any { // 快速版本：假设调用时已确保参数是1个字符串（无错误可能）
//		   return strings.ToUpper(arg.(string))  // 直接断言+处理，省去所有检查
//	   },
//	   Types: types(func(string) string), // 声明只接收字符串
//	}
//
//	当框架通过静态分析确认调用是安全的（比如 ToUpper("hello")，参数数量和类型都正确），会直接调用 Fast，跳过 Func 中的各种检查，速度更快。
//	当调用存在不确定性（比如参数是动态生成的），则调用 Func，通过错误处理保证程序稳定。
