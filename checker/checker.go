package checker

import (
	"fmt"
	"reflect"
	"regexp"

	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/builtin"
	. "github.com/expr-lang/expr/checker/nature"
	"github.com/expr-lang/expr/conf"
	"github.com/expr-lang/expr/file"
	"github.com/expr-lang/expr/parser"
)

// Check 对表达式语法树进行类型检查和验证。
//
// 核心功能：
//	- 遍历节点：访问语法树的每个节点
//	- 类型推断：推断每个节点的类型
//	- 类型检查：验证类型兼容性和操作合法性
//	- 错误报告：发现并报告类型错误

// Run visitors in a given config over the given tree
// runRepeatable controls whether to filter for only vistors that require multiple passes or not
//
// Run 函数保证：
//   - 普通 visitor 只跑一次。
//   - 可重复 visitor 会多次执行，直到 ShouldRepeat() 返回 false。
//
// 某些语法树修改可能需要多轮才能完成（例如，先展开某个语法结构，才能继续处理展开后的新节点），因此需要支持重复执行。
func runVisitors(tree *parser.Tree, config *conf.Config, runRepeatable bool) {
	for {
		more := false
		for _, v := range config.Visitors {

			// We need to perform types check, because some visitors may rely on
			// types information available in the tree.
			//
			// 每次执行 visitor 前做一次类型检查，因为某些 visitor 可能依赖 AST 上的类型信息。
			// 这里忽略返回值和错误，只是为了刷新类型信息。
			_, _ = Check(tree, config)

			// 判断 visitor 是否实现了 repeatable 接口，若是说明它可能需要多次遍历 AST 才能完全生效。
			r, repeatable := v.(interface {
				Reset()
				ShouldRepeat() bool
			})

			// 分两种情况：
			//	可重复 visitor (repeatable == true)
			//		只在 runRepeatable == true 时执行。
			//		执行前 Reset() 重置状态。
			//		遍历 AST 并应用 visitor。
			//		判断是否需要再次处理 AST。
			//		more = more || r.ShouldRepeat() 保证只要有一个 visitor 需要重复就继续循环。
			//	普通 visitor (repeatable == false)
			//		只在 runRepeatable == false 时执行。
			//		直接遍历 AST 应用 visitor。
			if repeatable {
				if runRepeatable {
					r.Reset()               // 重置 visitor 状态
					ast.Walk(&tree.Node, v) // 遍历语法树并应用 visitor
					more = more || r.ShouldRepeat()
				}
			} else {
				if !runRepeatable {
					ast.Walk(&tree.Node, v)
				}
			}
		}

		// 如果没有 visitor 表示需要重复处理 AST (more == false)，就跳出外层循环。
		if !more {
			break
		}
	}
}

// ParseCheck parses input expression and checks its types. Also, it applies
// all provided patchers. In case of error, it returns error with a tree.
func ParseCheck(input string, config *conf.Config) (*parser.Tree, error) {
	// 对输入 input 进行语法解析，得到 AST 语法树。
	tree, err := parser.ParseWithConfig(input, config)
	if err != nil {
		return tree, err
	}

	// 对 AST 语法树执行 visitor/patcher（访问器/补丁器）。
	// 分两步跑：
	//	- 先运行那些不能重复运行的（false），也就是单次 patch 的 visitor 。
	//	- 再运行需要多次修正的（true），比如运算符 patch（有些地方需要迭代多次调整 AST 才能确定正确结构，比如运算符优先级和结合性）。
	if len(config.Visitors) > 0 {
		// Run all patchers that don't support being run repeatedly first
		runVisitors(tree, config, false)
		// Run patchers that require multiple passes next (currently only Operator patching)
		runVisitors(tree, config, true)
	}

	// 对 AST 做类型检查。
	_, err = Check(tree, config)
	if err != nil {
		return tree, err
	}

	return tree, nil
}

// Check checks types of the expression tree. It returns type of the expression
// and error if any. If config is nil, then default configuration will be used.
//
// Check 函数是类型检查的入口点，它接收 AST 树和配置，进行全面的类型检查，并返回表达式的最终类型。
func Check(tree *parser.Tree, config *conf.Config) (reflect.Type, error) {
	if config == nil {
		config = conf.New(nil)
	}

	v := &checker{config: config}
	nt := v.visit(tree.Node)

	// To keep compatibility with previous versions, we should return any, if nature is unknown.
	// 兼容性处理：未知类型返回 interface{}
	t := nt.Type
	if t == nil {
		t = anyType
	}

	// 如果检查过程中遇到语法或类型错误，报错返回
	if v.err != nil {
		return t, v.err.Bind(tree.Source)
	}

	// 配置里声明了期望类型
	if v.config.Expect != reflect.Invalid {
		// 如果允许任何类型且当前类型未知，则通过检查，否则必须完全匹配期望类型
		if v.config.ExpectAny {
			if isUnknown(nt) {
				return t, nil
			}
		}
		switch v.config.Expect {
		case reflect.Int, reflect.Int64, reflect.Float64:
			if !isNumber(nt) {
				return nil, fmt.Errorf("expected %v, but got %v", v.config.Expect, nt)
			}
		default:
			if nt.Kind() != v.config.Expect {
				return nil, fmt.Errorf("expected %v, but got %s", v.config.Expect, nt)
			}
		}
	}

	return t, nil
}

type checker struct {
	config          *conf.Config     // 配置信息
	predicateScopes []predicateScope // 谓词作用域栈
	varScopes       []varScope       // 变量作用域栈
	err             *file.Error      // 错误信息
}

type predicateScope struct {
	collection Nature            // 集合类型
	vars       map[string]Nature // 变量类型
}

type varScope struct {
	name   string // 变量名
	nature Nature // 变量类型
}

type info struct {
	method bool
	fn     *builtin.Function

	// elem is element type of array or map.
	// Arrays created with type []any, but
	// we would like to detect expressions
	// like `42 in ["a"]` as invalid.
	elem reflect.Type
}

// 对所有 AST 节点类型进行检查
func (v *checker) visit(node ast.Node) Nature {
	var nt Nature
	switch n := node.(type) {
	case *ast.NilNode:
		nt = v.NilNode(n)
	case *ast.IdentifierNode:
		nt = v.IdentifierNode(n)
	case *ast.IntegerNode:
		nt = v.IntegerNode(n)
	case *ast.FloatNode:
		nt = v.FloatNode(n)
	case *ast.BoolNode:
		nt = v.BoolNode(n)
	case *ast.StringNode:
		nt = v.StringNode(n)
	case *ast.ConstantNode:
		nt = v.ConstantNode(n)
	case *ast.UnaryNode:
		nt = v.UnaryNode(n)
	case *ast.BinaryNode:
		nt = v.BinaryNode(n)
	case *ast.ChainNode:
		nt = v.ChainNode(n)
	case *ast.MemberNode:
		nt = v.MemberNode(n)
	case *ast.SliceNode:
		nt = v.SliceNode(n)
	case *ast.CallNode:
		nt = v.CallNode(n)
	case *ast.BuiltinNode:
		nt = v.BuiltinNode(n)
	case *ast.PredicateNode:
		nt = v.PredicateNode(n)
	case *ast.PointerNode:
		nt = v.PointerNode(n)
	case *ast.VariableDeclaratorNode:
		nt = v.VariableDeclaratorNode(n)
	case *ast.SequenceNode:
		nt = v.SequenceNode(n)
	case *ast.ConditionalNode:
		nt = v.ConditionalNode(n)
	case *ast.ArrayNode:
		nt = v.ArrayNode(n)
	case *ast.MapNode:
		nt = v.MapNode(n)
	case *ast.PairNode:
		nt = v.PairNode(n)
	default:
		panic(fmt.Sprintf("undefined node type (%T)", node))
	}
	node.SetNature(nt)
	return nt
}

func (v *checker) error(node ast.Node, format string, args ...any) Nature {
	if v.err == nil { // show first error
		v.err = &file.Error{
			Location: node.Location(),
			Message:  fmt.Sprintf(format, args...),
		}
	}
	return unknown
}

func (v *checker) NilNode(*ast.NilNode) Nature {
	return nilNature
}

func (v *checker) IdentifierNode(node *ast.IdentifierNode) Nature {
	// 先在局部变量中查找，找到直接返回
	if variable, ok := v.lookupVariable(node.Value); ok {
		return variable.nature
	}
	// 如果查找 $env ，返回 unknown
	if node.Value == "$env" {
		return unknown
	}

	// 然后在 env/function/builtin 中查找
	return v.ident(node, node.Value, v.config.Strict, true)
}

// ident method returns type of environment variable, builtin or function.
//
// ident 在 env/function/builtin 中查找和确定标识符的类型信息。
// 参数说明：
// ∙ node: 当前 AST 节点
// ∙ name: 要查找的标识符名称
// ∙ strict: 是否启用严格模式（找不到时是否报错）
// ∙ builtins: 是否查找内置函数
func (v *checker) ident(node ast.Node, name string, strict, builtins bool) Nature {
	// 首先在环境变量中查找标识符
	//	∙ v.config.Env 是预先配置的类型环境，通常包含全局变量和自定义函数，找到直接返回对应的 Nature 类型
	if nt, ok := v.config.Env.Get(name); ok {
		return nt
	}

	// 当 builtins 参数为 true 时，检查内置函数
	//	∙ 先在用户自定义函数 (Functions) 中查找
	//	∙ 然后在系统内置函数 (Builtins) 中查找
	//	∙ 返回的函数类型包含函数签名和函数对象本身
	if builtins {
		if fn, ok := v.config.Functions[name]; ok {
			return Nature{Type: fn.Type(), Func: fn}
		}
		if fn, ok := v.config.Builtins[name]; ok {
			return Nature{Type: fn.Type(), Func: fn}
		}
	}

	// 标识符未找到，在严格模式下报错
	if v.config.Strict && strict {
		return v.error(node, "unknown name %v", name)
	}

	// 非严格模式，返回 unknown
	return unknown
}

func (v *checker) IntegerNode(*ast.IntegerNode) Nature {
	return integerNature
}

func (v *checker) FloatNode(*ast.FloatNode) Nature {
	return floatNature
}

func (v *checker) BoolNode(*ast.BoolNode) Nature {
	return boolNature
}

func (v *checker) StringNode(*ast.StringNode) Nature {
	return stringNature
}

func (v *checker) ConstantNode(node *ast.ConstantNode) Nature {
	return Nature{Type: reflect.TypeOf(node.Value)}
}

func (v *checker) UnaryNode(node *ast.UnaryNode) Nature {
	nt := v.visit(node.Node)
	nt = nt.Deref()

	switch node.Operator {

	case "!", "not":
		if isBool(nt) {
			return boolNature
		}
		if isUnknown(nt) {
			return boolNature
		}

	case "+", "-":
		if isNumber(nt) {
			return nt
		}
		if isUnknown(nt) {
			return unknown
		}

	default:
		return v.error(node, "unknown operator (%v)", node.Operator)
	}

	return v.error(node, `invalid operation: %v (mismatched type %s)`, node.Operator, nt)
}

// BinaryNode 作用：
//   - 类型检查：验证左右操作数的类型兼容性
//   - 结果推断：推断运算结果的类型
//   - 错误检测：发现并报告类型不匹配的错误
func (v *checker) BinaryNode(node *ast.BinaryNode) Nature {
	l := v.visit(node.Left)  // 检查左操作数
	r := v.visit(node.Right) // 检查右操作数

	l = l.Deref() // 解引用指针类型
	r = r.Deref() // 解引用指针类型

	switch node.Operator {
	case "==", "!=": // bool
		if isComparable(l, r) { // 检查是否可比较
			return boolNature
		}

	case "or", "||", "and", "&&": // bool
		if isBool(l) && isBool(r) { // 两个操作数都必须是布尔类型
			return boolNature
		}
		if or(l, r, isBool) {
			return boolNature
		}

	case "<", ">", ">=", "<=": // bool
		if isNumber(l) && isNumber(r) {
			return boolNature
		}
		if isString(l) && isString(r) {
			return boolNature
		}
		if isTime(l) && isTime(r) {
			return boolNature
		}
		if isDuration(l) && isDuration(r) {
			return boolNature
		}
		if or(l, r, isNumber, isString, isTime, isDuration) {
			return boolNature
		}

	case "-":
		if isNumber(l) && isNumber(r) {
			return combined(l, r)
		}
		if isTime(l) && isTime(r) {
			return durationNature
		}
		if isTime(l) && isDuration(r) {
			return timeNature
		}
		if isDuration(l) && isDuration(r) {
			return durationNature
		}
		if or(l, r, isNumber, isTime, isDuration) {
			return unknown
		}

	case "*":
		if isNumber(l) && isNumber(r) {
			return combined(l, r)
		}
		if isNumber(l) && isDuration(r) {
			return durationNature
		}
		if isDuration(l) && isNumber(r) {
			return durationNature
		}
		if isDuration(l) && isDuration(r) {
			return durationNature
		}
		if or(l, r, isNumber, isDuration) {
			return unknown
		}

	case "/":
		if isNumber(l) && isNumber(r) {
			return floatNature
		}
		if or(l, r, isNumber) {
			return floatNature
		}

	case "**", "^":
		if isNumber(l) && isNumber(r) {
			return floatNature
		}
		if or(l, r, isNumber) {
			return floatNature
		}

	case "%":
		if isInteger(l) && isInteger(r) {
			return integerNature
		}
		if or(l, r, isInteger) {
			return integerNature
		}

	case "+":
		if isNumber(l) && isNumber(r) {
			return combined(l, r)
		}
		if isString(l) && isString(r) {
			return stringNature
		}
		if isTime(l) && isDuration(r) {
			return timeNature
		}
		if isDuration(l) && isTime(r) {
			return timeNature
		}
		if isDuration(l) && isDuration(r) {
			return durationNature
		}
		if or(l, r, isNumber, isString, isTime, isDuration) {
			return unknown
		}

	case "in":
		// in ：
		//	∙ x in struct → 检查字段名是否在结构体里，返回 bool。
		//	∙ x in map    → 检查 Map 键类型是否匹配。
		//	∙ x in array  → 检查 Array 元素类型是否可比较。
		if (isString(l) || isUnknown(l)) && isStruct(r) {
			return boolNature
		}
		if isMap(r) {
			if !isUnknown(l) && !l.AssignableTo(r.Key()) {
				return v.error(node, "cannot use %v as type %v in map key", l, r.Key())
			}
			return boolNature
		}
		if isArray(r) {
			if !isComparable(l, r.Elem()) {
				return v.error(node, "cannot use %v as type %v in array", l, r.Elem())
			}
			return boolNature
		}
		if isUnknown(l) && anyOf(r, isString, isArray, isMap) {
			return boolNature
		}
		if isUnknown(r) {
			return boolNature
		}

	case "matches":
		// 要求右操作数为字符串，且是合法正则表达式。
		if s, ok := node.Right.(*ast.StringNode); ok {
			_, err := regexp.Compile(s.Value)
			if err != nil {
				return v.error(node, err.Error())
			}
		}
		if isString(l) && isString(r) {
			return boolNature
		}
		if or(l, r, isString) {
			return boolNature
		}

	case "contains", "startsWith", "endsWith":
		if isString(l) && isString(r) {
			return boolNature
		}
		if or(l, r, isString) {
			return boolNature
		}

	case "..":
		if isInteger(l) && isInteger(r) {
			return arrayOf(integerNature)
		}
		if or(l, r, isInteger) {
			return arrayOf(integerNature)
		}

	case "??":
		if isNil(l) && !isNil(r) {
			return r
		}
		if !isNil(l) && isNil(r) {
			return l
		}
		if isNil(l) && isNil(r) {
			return nilNature
		}
		if r.AssignableTo(l) {
			return l
		}
		return unknown

	default:
		return v.error(node, "unknown operator (%v)", node.Operator)

	}

	return v.error(node, `invalid operation: %v (mismatched types %v and %v)`, node.Operator, l, r)
}

func (v *checker) ChainNode(node *ast.ChainNode) Nature {
	return v.visit(node.Node)
}

// MemberNode 根据基节点类型（如 $env、map、数组、结构体等）和属性信息推断成员类型，返回对应字段/方法的类型 (Nature) 或者报错。
func (v *checker) MemberNode(node *ast.MemberNode) Nature {
	// 如果基节点是 $env ，则属性必须是字符串字面量：$env."foo" ，否则返回 unknown 。
	// 根据属性名 "foo" 去 $env 中查找（不启用 builtins/functions），如果加了 optional 标志（如 $env?."foo"），即使不存在也不报错。
	if an, ok := node.Node.(*ast.IdentifierNode); ok && an.Value == "$env" {
		if name, ok := node.Property.(*ast.StringNode); ok {
			strict := v.config.Strict
			if node.Optional {
				// If user explicitly set optional flag, then we should not
				// throw error if field is not found (as user trying to handle
				// this case). But if user did not set optional flag, then we
				// should throw error if field is not found & v.config.Strict.
				strict = false
			}
			return v.ident(node, name.Value, strict, false /* no builtins and no functions */)
		}
		return unknown
	}

	// 如果基节点不是 $env ，按普通节点处理。

	base := v.visit(node.Node)     // 先推断基对象类型
	prop := v.visit(node.Property) // 再推断属性类型

	if isUnknown(base) { // 如果 base 是未知类型，直接返回 unknown。
		return unknown
	}

	// 如果属性名是字符串字面量，优先按成员方法查找
	if name, ok := node.Property.(*ast.StringNode); ok {
		// 不能在 nil 上取字段。
		if isNil(base) {
			return v.error(node, "type nil has no field %v", name.Value)
		}

		// First, check methods defined on base type itself,
		// independent of which type it is. Without dereferencing.
		//
		// 检查和获取成员方法的类型信息（备注：不解引用，直接查方法，因为方法可能定义在 *T 上）
		if m, ok := base.MethodByName(name.Value); ok {
			return m
		}
	}

	// 指针解引用，获取底层类型
	base = base.Deref()

	switch base.Kind() {
	case reflect.Map:
		// 检查 prop 和 map 的键类型是否匹配，不匹配则报错
		if !prop.AssignableTo(base.Key()) && !isUnknown(prop) {
			return v.error(node.Property, "cannot use %v to get an element from %v", prop, base)
		}

		// 如果 prop 是字符串字面量，则先在 base.Fields（静态字段表）里查，如果没找到且开启 Strict 模式则报错。
		if prop, ok := node.Property.(*ast.StringNode); ok {
			if field, ok := base.Fields[prop.Value]; ok {
				return field
			} else if base.Strict {
				return v.error(node.Property, "unknown field %v", prop.Value)
			}
		}

		// 否则，直接返回 map 的值类型，作为默认类型返回
		return base.Elem()

	case reflect.Array, reflect.Slice:
		// 检查：对于数组来说，prop 必须是整数或者 unknown ，因为它是作为索引下标来用的。
		if !isInteger(prop) && !isUnknown(prop) {
			return v.error(node.Property, "array elements can only be selected using an integer (got %v)", prop)
		}
		// 推断：返回数组元素类型
		return base.Elem()

	case reflect.Struct:
		// 对于结构体，属性必须是字符串字面量。
		if name, ok := node.Property.(*ast.StringNode); ok {
			propertyName := name.Value
			// 在结构体中查找目标字段
			if field, ok := base.FieldByName(propertyName); ok {
				return Nature{Type: field.Type}
			}
			if node.Method {
				return v.error(node, "type %v has no method %v", base, propertyName)
			}
			return v.error(node, "type %v has no field %v", base, propertyName)
		}
	}

	// Not found.

	if name, ok := node.Property.(*ast.StringNode); ok {
		if node.Method {
			return v.error(node, "type %v has no method %v", base, name.Value)
		}
		return v.error(node, "type %v has no field %v", base, name.Value)
	}

	return v.error(node, "type %v[%v] is undefined", base, prop)
}

func (v *checker) SliceNode(node *ast.SliceNode) Nature {
	// 推断数组类型
	nt := v.visit(node.Node)

	if isUnknown(nt) {
		return unknown
	}

	switch nt.Kind() {
	case reflect.String, reflect.Array, reflect.Slice:
		// ok
	default:
		return v.error(node, "cannot slice %s", nt)
	}

	// 推断 From 类型，要求其必须是整型
	if node.From != nil {
		from := v.visit(node.From)
		if !isInteger(from) && !isUnknown(from) {
			return v.error(node.From, "non-integer slice index %v", from)
		}
	}

	// 推断 To 类型，要求其必须是整型
	if node.To != nil {
		to := v.visit(node.To)
		if !isInteger(to) && !isUnknown(to) {
			return v.error(node.To, "non-integer slice index %v", to)
		}
	}

	return nt
}

func (v *checker) CallNode(node *ast.CallNode) Nature {
	nt := v.functionReturnType(node)

	// Check if type was set on node (for example, by patcher)
	// and use node type instead of function return type.
	//
	// If node type is anyType, then we should use function
	// return type. For example, on error we return anyType
	// for a call `errCall().Method()` and method will be
	// evaluated on `anyType.Method()`, so return type will
	// be anyType `anyType.Method(): anyType`. Patcher can
	// fix `errCall()` to return proper type, so on second
	// checker pass we should replace anyType on method node
	// with new correct function return type.
	if node.Type() != nil && node.Type() != anyType {
		return node.Nature()
	}

	return nt
}

// functionReturnType() 作用：
// ∙ 确定被调对象的类型：函数、方法或可调用对象
// ∙ 检查函数调用合法性：验证是否可以调用
// ∙ 验证参数匹配：检查参数类型和数量是否正确
// ∙ 推断返回类型：根据函数定义推断返回值类型
//
// 支持函数类型：
// 1. 函数调用：myFunction(arg1, arg2)  // IdentifierNode
// 2. 方法调用：obj.method(arg1, arg2)  // MemberNode
// 3. 函数表达式调用：(func(x int) int { return x * 2 })(5)  // 匿名函数调用
// 4. 内置函数调用：len(array)  // 内置函数
func (v *checker) functionReturnType(node *ast.CallNode) Nature {
	// 先检查和推导被调对象 node.Callee ，如 foo() 里的 foo，或者 obj.bar() 里的 obj.bar，得到 Nature 。
	nt := v.visit(node.Callee)

	// 如果 nt 中 Func 非空，说明这是个已知的、预定义的函数（内置函数、用户注册函数、特殊优化函数），直接调用 checkFunction 检查参数并返回类型。
	// 这是对已知的、预定义的函数的简化处理，而普通的 func 走 reflect.Func 分支。
	//
	// 备注：设置 nt.Func 的地方在 checker.ident() 函数中。
	if nt.Func != nil {
		return v.checkFunction(nt.Func, node, node.Arguments)
	}

	// 如果 Callee 是标识符，如 foo() 中的 foo ，就取标识符名 foo 作为 fnName 。
	// 如果 Callee 是对象成员调用，如 obj.bar() 里的 obj.bar ，就取成员名字 bar 作为 fnName 。
	fnName := "function"
	if identifier, ok := node.Callee.(*ast.IdentifierNode); ok {
		fnName = identifier.Value
	}
	if member, ok := node.Callee.(*ast.MemberNode); ok {
		if name, ok := member.Property.(*ast.StringNode); ok {
			fnName = name.Value
		}
	}

	// 如果 Callee 的类型是 unknown，没法判断，那返回 unknown。
	if isUnknown(nt) {
		return unknown
	}
	// 如果 Callee 的类型是 nil，报错。
	if isNil(nt) {
		return v.error(node, "%v is nil; cannot call nil as function", fnName) // 禁止调用 nil
	}

	// 如果 nt 是函数类型(reflect.Func)：
	//	- 调用 checkArguments 校验实参类型是否匹配函数签名；
	//	- 返回函数的返回值类型（outType）；
	//	- 如果报错，记录错误并返回 unknown。
	// 否则，报错：xxx is not callable。
	switch nt.Kind() {
	case reflect.Func:
		outType, err := v.checkArguments(fnName, nt, node.Arguments, node)
		if err != nil {
			if v.err == nil {
				v.err = err
			}
			return unknown
		}
		return outType
	}
	return v.error(node, "%s is not callable", nt)
}

// 为什么谓词需要作用域管理?
//
//
// 在函数式编程中，高阶函数的谓词通常是匿名函数（lambda表达式）：
//   all(users, user => user.age > 18)
// 这里，user 变量不是预先声明的，它的存在和类型完全依赖于上下文。
//
// 在普通编程语言里：
//	func(x int) bool { return x > 0 }
// 这里 x 的类型 int 是显式声明的，编译器天然知道 x 是 int。
//
// 在 DSL 里，我们经常写简化版：
//	filter([1,2,3], func(x){ x > 0 })
// 这里 x 没有写类型，就要靠上下文（也就是外层集合的元素类型）来推断。
//
// 当 checker 遍历到 func(x){...} 时，它必须知道：
//	- x 的类型是什么？（int？string？还是 unknown？）
//	- 有没有额外变量，这些变量的类型是什么？比如 map 的 index，reduce 的 acc。
//		- map(arr, func(x, i){ x + i })
//		- reduce(arr, func(x, acc){ x + acc }, 0)
// 这些变量都不是全局的，而是内置函数在检查时 “注入” 的。
//
// 谓词的特殊性在于：它依赖动态、临时、与集合强绑定的变量，且可能存在嵌套和重名。作用域管理通过以下方式解决这些问题：
//	- 临时注册变量：为 item、index 等动态变量提供类型定义，避免 “未定义变量” 错误；
//	- 隔离上下文：用栈结构确保不同谓词的变量不冲突，避免全局污染；
//	- 动态绑定类型：随集合类型同步更新变量类型，确保类型检查的准确性；
//	- 支持嵌套场景：通过栈的 “压入 / 弹出” 传递多层上下文，确保嵌套谓词的检查正确。
// 简单来说：没有作用域管理，类型检查器就 “看不懂” 谓词中的临时变量，无法验证谓词逻辑的合法性 —— 作用域管理是谓词能被正确检查的前提。
//

// 具体原因
//
// 原因一：谓词依赖 “动态生成的临时变量”，无作用域则无法识别。
//
// 谓词函数的核心逻辑依赖「与集合强关联的临时变量」，这些变量的定义和类型是 “动态的”（由集合类型决定），而非预先定义的全局 / 局部变量。
// 示例：map([1,2,3], item => item * 2) 中的 item 是 map 函数遍历数组时 “动态生成” 的变量，代表数组的当前元素，它的类型由数组元素类型决定（这里是整数）。
// 如果没有作用域管理：类型检查器在处理谓词 item => item * 2 时，会认为 item 是 “未定义的变量”，直接报错（因为找不到 item 的类型定义）。
// 如果有作用域管理时：调用 v.begin(collection) 时，会将「集合的元素类型」注册到临时作用域中（隐含 item 的类型 = 集合元素类型）。
// 类型检查器从作用域中查询到 item 是整数类型，才能验证 item * 2 是合法运算（整数 × 整数）。
//
// 原因二：临时变量需 “隔离”，避免全局污染或冲突
//
// 不同谓词的临时变量可能重名（比如两个 map 都用 index 作为索引变量），若没有作用域隔离，会导致变量类型 “串用”，引发类型检查错误。
//
// 示例：两个 map 嵌套的场景
//	map([1,2,3], func(item1 int, index int) {  // 外层 index（整数）
//    return map(["a","b"], func(item2 string, index int) {  // 内层 index（整数）
//        return item1 + index  // 这里的 index 是外层还是内层的？
//    })
//	})
// 	外层和内层的索引变量都被显式命名为 index（开发者疏忽导致重名）；
//	若没有作用域管理，类型检查器会将两个 index 视为 “同一个变量”，导致：
//	变量类型冲突：如果外层 index 是整数、内层 index 被误改为字符串（比如 index string），类型检查器会认为 “同一个变量同时是整数和字符串”，直接报类型冲突错误（但实际是两个不同作用域的变量，本不应冲突）；
//	逻辑误判：若内层 index 是整数，但开发者实际想使用外层 index（比如 item1 + 外层index），类型检查器无法区分，可能导致逻辑错误（比如本应加外层索引 2，却加了内层索引 0）。
//
//	有作用域管理时：
//	两个 index 属于不同的作用域（外层作用域 vs 内层作用域），作用域栈会确保 “内层作用域的变量优先被查询”，同时外层变量不会被内层覆盖；
//	即使变量名相同，类型检查器也能通过作用域区分它们的上下文，避免 “同一变量类型冲突” 的误判，同时确保开发者使用的是当前作用域的变量（符合预期）。
//
// 原因三：变量类型需 “动态绑定”，随集合类型变化
//
// 谓词的临时变量类型与集合类型强绑定（比如数组元素类型变了，item 类型也会变），作用域管理能动态同步这种绑定关系，确保类型检查的准确性。
// 示例：map 处理不同类型的数组
//	- 场景 1：map([1,2,3], item => item * 2) → item 是整数，item * 2 合法；
//	- 场景 2：map(["a","b"], item => item * 2) → item 是字符串，item * 2 非法（字符串不能做乘法）。
// 若无作用域管理：类型检查器无法动态关联 item 和数组元素类型，可能将 item 固定为某一类型（比如整数），导致场景 2 的错误无法被识别（误判 item 是整数，认为 item * 2 合法）。
// 有作用域管理时：每次调用 begin 都会将当前集合的类型（如场景 1 的 “整数数组”、场景 2 的 “字符串数组”）注册到作用域，item 的类型会动态同步为集合元素类型，从而在场景 2 中准确发现 “字符串 × 整数” 的错误。
//
// 原因四：支持 “嵌套谓词” 的上下文正确传递
//
// 当内置函数的谓词中嵌套了另一个需要谓词的内置函数（如 map 中嵌套 filter），作用域管理通过 “栈结构”（predicateScopes）确保多层上下文的正确传递。
// 示例：map([1,2,3,4], item => filter([item, item*2], subItem => subItem > 2))
// 外层是 map（处理 [1,2,3,4]），内层是 filter（处理 [item, item*2]）；
// 外层谓词的 item（整数）和内层谓词的 subItem（整数）属于不同层级的临时变量。
// 作用域栈的变化过程：
//	- 进入外层 map → begin 创建 “外层作用域”（item: 整数），栈：[外层作用域]；
//	- 进入内层 filter → begin 创建 “内层作用域”（subItem: 整数），栈：[外层作用域, 内层作用域]；
//	- 处理内层谓词 subItem => subItem > 2 → 从栈顶（内层作用域）查询 subItem 类型（整数），验证 subItem > 2 合法；
//	- 退出内层 filter → end 销毁内层作用域，栈：[外层作用域]；
//	- 退出外层 map → end 销毁外层作用域，栈为空。
// 若无作用域栈：
//	- 内层谓词的 subItem 会与外层的 item 混淆，或无法找到 subItem 的类型定义，导致嵌套场景的类型检查完全失效。

// BuiltinNode 校验内置函数（all、map、reduce、filter 等）的参数类型，获取其返回类型。
func (v *checker) BuiltinNode(node *ast.BuiltinNode) Nature {
	switch node.Name {
	// 功能说明：
	// ∙ all：检查集合中所有元素是否满足谓词条件
	// ∙ none：检查集合中没有任何元素满足谓词条件
	// ∙ any：检查集合中至少有一个元素满足谓词条件
	// ∙ one：检查集合中恰好有一个元素满足谓词条件
	case "all", "none", "any", "one":

		// node.Arguments[0] 是函数调用的第一个参数，一般是数组类型。
		// v.visit(...) 会返回这个参数的 Nature 类型信息。
		// Deref() 会获取数组本身的类型（去掉指针/包装类型），方便做后续类型检查。
		collection := v.visit(node.Arguments[0]).Deref()

		// all/any/none/one 的第一个参数必须是数组，否则报错。
		// isUnknown(collection) 是防御性处理：如果类型未知，则先允许通过，不报错。
		if !isArray(collection) && !isUnknown(collection) {
			return v.error(node.Arguments[0], "builtin %v takes only array (got %v)", node.Name, collection)
		}

		// v.begin(collection)：把集合元素放到当前作用域里，供 predicate 使用。
		// node.Arguments[1] 是谓词函数（predicate）。
		// v.visit(...) 推断 predicate 的类型。
		// v.end()：离开作用域，撤销对集合元素的临时绑定。
		v.begin(collection)
		predicate := v.visit(node.Arguments[1])
		v.end()

		// isFunc(predicate)：必须是函数类型。
		// predicate.NumIn() == 1：函数必须接收 1 个参数（集合元素）。
		// predicate.NumOut() == 1：函数必须返回 1 个结果。
		// isUnknown(predicate.In(0))：输入参数的类型应为「未知」（这通常是因为在作用域中，输入参数是集合的元素，其类型需要动态推断）。

		// 换句话说，predicate 应该是 func(elem) bool 的形式。
		if isFunc(predicate) &&
			predicate.NumOut() == 1 &&
			predicate.NumIn() == 1 &&
			isUnknown(predicate.In(0)) {

			// all/any/none/one 的 predicate 必须返回布尔值。
			// isUnknown 依然是防御性处理：如果返回类型未知也允许通过。
			if !isBool(predicate.Out(0)) && !isUnknown(predicate.Out(0)) {
				return v.error(node.Arguments[1], "predicate should return boolean (got %v)", predicate.Out(0).String())
			}

			// all/any/none/one 的返回类型都是布尔型。
			return boolNature
		}

		// 如果 predicate 不符合签名，报错：predicate 必须是 1 入 1 出的函数。
		return v.error(node.Arguments[1], "predicate should has one input and one output param")

	case "filter":
		collection := v.visit(node.Arguments[0]).Deref()
		if !isArray(collection) && !isUnknown(collection) {
			return v.error(node.Arguments[0], "builtin %v takes only array (got %v)", node.Name, collection)
		}

		v.begin(collection)
		predicate := v.visit(node.Arguments[1])
		v.end()

		if isFunc(predicate) &&
			predicate.NumOut() == 1 &&
			predicate.NumIn() == 1 && isUnknown(predicate.In(0)) {

			if !isBool(predicate.Out(0)) && !isUnknown(predicate.Out(0)) {
				return v.error(node.Arguments[1], "predicate should return boolean (got %v)", predicate.Out(0).String())
			}
			if isUnknown(collection) {
				return arrayNature
			}
			return arrayOf(collection.Elem())
		}
		return v.error(node.Arguments[1], "predicate should has one input and one output param")

	case "map":
		// 假设我们要对 `map([1,2,3], (item, index) => item * 2)` 做类型检查

		// 1. 处理第一个参数（集合：[1,2,3]），获取其类型（整数数组）
		collection := v.visit(node.Arguments[0]).Deref()
		if !isArray(collection) && !isUnknown(collection) {
			return v.error(node.Arguments[0], "builtin %v takes only array (got %v)", node.Name, collection)
		}

		// 2. 创建临时作用域：注册集合类型 collection 和变量 index 及类型
		v.begin(collection, scopeVar{"index", integerNature})
		// 3. 检查谓词函数，这一步必须依赖 begin 创建的临时作用域，否则无法识别 item 和 index 的类型：
		//  - 识别 index 变量的类型：
		//		谓词函数中使用了 index 变量，类型检查器会从当前栈顶的作用域（即 begin 创建的 predicateScope）中查询 index 的类型：
		//		- 访问 v.predicateScopes[len(v.predicateScopes)-1].vars["index"] → 得到 integerNature（整数类型）。
		//		- 确认 index 是整数类型，符合预期（索引只能是整数）。
		//	- 识别 item 变量的类型：
		//		谓词函数的第一个参数 item 是 “集合的元素”，类型检查器会从栈顶作用域的 collection 字段中获取元素类型：
		//		- collection 是 “整数数组”，调用 collection.Elem() → 得到 integerNature（整数类型）。
		//		- 确认 item 是整数类型，后续执行 item * 2 时，能验证 “整数 × 整数” 是合法运算（不会出现 “字符串 × 整数” 的错误）。
		//	- 验证谓词函数的合法性：
		//		通过作用域确认 item（整数）和 index（整数）的类型后，继续检查谓词函数的输入/输出：
		//		- 输入参数数量：2 个（item 和 index），符合 map 谓词的预期；
		//		- 输出参数数量：1 个（item * 2 的结果，整数类型），符合 map 转换的预期；
		//		- 最终确认谓词函数合法。
		predicate := v.visit(node.Arguments[1])
		// 4. 关闭临时作用域
		v.end()

		// 5. 检查谓词函数是否合法，返回新数组类型
		if isFunc(predicate) &&
			predicate.NumOut() == 1 &&
			predicate.NumIn() == 1 && isUnknown(predicate.In(0)) {

			return arrayOf(*predicate.PredicateOut)
		}
		return v.error(node.Arguments[1], "predicate should has one input and one output param")

	case "count":
		collection := v.visit(node.Arguments[0]).Deref()
		if !isArray(collection) && !isUnknown(collection) {
			return v.error(node.Arguments[0], "builtin %v takes only array (got %v)", node.Name, collection)
		}

		if len(node.Arguments) == 1 {
			return integerNature
		}

		v.begin(collection)
		predicate := v.visit(node.Arguments[1])
		v.end()

		if isFunc(predicate) &&
			predicate.NumOut() == 1 &&
			predicate.NumIn() == 1 && isUnknown(predicate.In(0)) {
			if !isBool(predicate.Out(0)) && !isUnknown(predicate.Out(0)) {
				return v.error(node.Arguments[1], "predicate should return boolean (got %v)", predicate.Out(0).String())
			}

			return integerNature
		}
		return v.error(node.Arguments[1], "predicate should has one input and one output param")

	case "sum":
		collection := v.visit(node.Arguments[0]).Deref()
		if !isArray(collection) && !isUnknown(collection) {
			return v.error(node.Arguments[0], "builtin %v takes only array (got %v)", node.Name, collection)
		}

		if len(node.Arguments) == 2 {
			v.begin(collection)
			predicate := v.visit(node.Arguments[1])
			v.end()

			if isFunc(predicate) &&
				predicate.NumOut() == 1 &&
				predicate.NumIn() == 1 && isUnknown(predicate.In(0)) {
				return predicate.Out(0)
			}
		} else {
			if isUnknown(collection) {
				return unknown
			}
			return collection.Elem()
		}

	case "find", "findLast":
		collection := v.visit(node.Arguments[0]).Deref()
		if !isArray(collection) && !isUnknown(collection) {
			return v.error(node.Arguments[0], "builtin %v takes only array (got %v)", node.Name, collection)
		}

		v.begin(collection)
		predicate := v.visit(node.Arguments[1])
		v.end()

		if isFunc(predicate) &&
			predicate.NumOut() == 1 &&
			predicate.NumIn() == 1 && isUnknown(predicate.In(0)) {

			if !isBool(predicate.Out(0)) && !isUnknown(predicate.Out(0)) {
				return v.error(node.Arguments[1], "predicate should return boolean (got %v)", predicate.Out(0).String())
			}
			if isUnknown(collection) {
				return unknown
			}
			return collection.Elem()
		}
		return v.error(node.Arguments[1], "predicate should has one input and one output param")

	case "findIndex", "findLastIndex":
		collection := v.visit(node.Arguments[0]).Deref()
		if !isArray(collection) && !isUnknown(collection) {
			return v.error(node.Arguments[0], "builtin %v takes only array (got %v)", node.Name, collection)
		}

		v.begin(collection)
		predicate := v.visit(node.Arguments[1])
		v.end()

		if isFunc(predicate) &&
			predicate.NumOut() == 1 &&
			predicate.NumIn() == 1 && isUnknown(predicate.In(0)) {

			if !isBool(predicate.Out(0)) && !isUnknown(predicate.Out(0)) {
				return v.error(node.Arguments[1], "predicate should return boolean (got %v)", predicate.Out(0).String())
			}
			return integerNature
		}
		return v.error(node.Arguments[1], "predicate should has one input and one output param")

	case "groupBy":
		collection := v.visit(node.Arguments[0]).Deref()
		if !isArray(collection) && !isUnknown(collection) {
			return v.error(node.Arguments[0], "builtin %v takes only array (got %v)", node.Name, collection)
		}

		v.begin(collection)
		predicate := v.visit(node.Arguments[1])
		v.end()

		if isFunc(predicate) &&
			predicate.NumOut() == 1 &&
			predicate.NumIn() == 1 && isUnknown(predicate.In(0)) {

			groups := arrayOf(collection.Elem())
			return Nature{Type: reflect.TypeOf(map[any][]any{}), ArrayOf: &groups}
		}
		return v.error(node.Arguments[1], "predicate should has one input and one output param")

	case "sortBy":
		collection := v.visit(node.Arguments[0]).Deref()
		if !isArray(collection) && !isUnknown(collection) {
			return v.error(node.Arguments[0], "builtin %v takes only array (got %v)", node.Name, collection)
		}

		v.begin(collection)
		predicate := v.visit(node.Arguments[1])
		v.end()

		if len(node.Arguments) == 3 {
			_ = v.visit(node.Arguments[2])
		}

		if isFunc(predicate) &&
			predicate.NumOut() == 1 &&
			predicate.NumIn() == 1 && isUnknown(predicate.In(0)) {

			return collection
		}
		return v.error(node.Arguments[1], "predicate should has one input and one output param")

	case "reduce":
		collection := v.visit(node.Arguments[0]).Deref()
		if !isArray(collection) && !isUnknown(collection) {
			return v.error(node.Arguments[0], "builtin %v takes only array (got %v)", node.Name, collection)
		}

		v.begin(collection, scopeVar{"index", integerNature}, scopeVar{"acc", unknown})
		predicate := v.visit(node.Arguments[1])
		v.end()

		if len(node.Arguments) == 3 {
			_ = v.visit(node.Arguments[2])
		}

		if isFunc(predicate) && predicate.NumOut() == 1 {
			return *predicate.PredicateOut
		}
		return v.error(node.Arguments[1], "predicate should has two input and one output param")

	}

	if id, ok := builtin.Index[node.Name]; ok {
		switch node.Name {
		case "get":
			return v.checkBuiltinGet(node)
		}
		return v.checkFunction(builtin.Builtins[id], node, node.Arguments)
	}

	return v.error(node, "unknown builtin %v", node.Name)
}

// scopeVar 表示一个作用域变量，用于定义「临时作用域中需要注册的变量」，存储变量名和对应的类型信息。
type scopeVar struct {
	varName   string // 变量名
	varNature Nature // 变量类型
}

// begin 方法用于创建一个新的谓词作用域，并将其压入作用域栈（predicateScopes），为后续处理谓词函数提供变量上下文。
func (v *checker) begin(collectionNature Nature, vars ...scopeVar) {
	// 1. 创建一个新的谓词作用域（predicateScope）
	scope := predicateScope{
		collection: collectionNature,        // 关联的集合类型（如数组的 Nature）
		vars:       make(map[string]Nature), // 存储当前作用域的变量（变量名→类型）
	}
	// 2. 将传入的临时变量（vars）注册到当前作用域中
	for _, v := range vars {
		scope.vars[v.varName] = v.varNature
	}
	// 3. 将新作用域压入作用域栈（predicateScopes），成为当前活跃作用域
	v.predicateScopes = append(v.predicateScopes, scope)
}

func (v *checker) end() {
	v.predicateScopes = v.predicateScopes[:len(v.predicateScopes)-1]
}

func (v *checker) checkBuiltinGet(node *ast.BuiltinNode) Nature {
	if len(node.Arguments) != 2 {
		return v.error(node, "invalid number of arguments (expected 2, got %d)", len(node.Arguments))
	}

	base := v.visit(node.Arguments[0])
	prop := v.visit(node.Arguments[1])

	if id, ok := node.Arguments[0].(*ast.IdentifierNode); ok && id.Value == "$env" {
		if s, ok := node.Arguments[1].(*ast.StringNode); ok {
			if nt, ok := v.config.Env.Get(s.Value); ok {
				return nt
			}
		}
		return unknown
	}

	if isUnknown(base) {
		return unknown
	}

	switch base.Kind() {
	case reflect.Slice, reflect.Array:
		if !isInteger(prop) && !isUnknown(prop) {
			return v.error(node.Arguments[1], "non-integer slice index %s", prop)
		}
		return base.Elem()
	case reflect.Map:
		if !prop.AssignableTo(base.Key()) && !isUnknown(prop) {
			return v.error(node.Arguments[1], "cannot use %s to get an element from %s", prop, base)
		}
		return base.Elem()
	}
	return v.error(node.Arguments[0], "type %v does not support indexing", base)
}

// checkFunction 检查内置函数（builtin.Function）调用的合法性，验证传入的参数类型是否符合函数的类型要求，并返回函数调用的结果类型（Nature）。
//
// 例子 1：有自定义验证器的函数（Validate != nil）
//
// 假设我们有一个函数 len(x)，它只能接收 slice 或 string 类型：
//
//	lenFunc := &builtin.Function{
//	   Name: "len",
//	   Validate: func(args []reflect.Type) (reflect.Type, error) {
//	       if len(args) != 1 {
//	           return nil, fmt.Errorf("len expects 1 argument")
//	       }
//	       if args[0].Kind() != reflect.Slice && args[0].Kind() != reflect.String {
//	           return nil, fmt.Errorf("len argument must be slice or string")
//	       }
//	       return reflect.TypeOf(0), nil // 返回 int
//	   },
//	}
//
// 调用：
//
//	callNode := &ast.CallNode{
//	   Callee: "len",
//	   Arguments: []ast.Node{ /* AST 节点代表 slice */ },
//	}
//
// nature := checker.checkFunction(lenFunc, callNode, callNode.Arguments)
//
// 流程：
// ∙ Validate != nil → 调用 lenFunc.Validate 检查参数。
// ∙ 参数合法 → 返回 Nature{Type: int}。
// ∙ 参数不合法 → 返回 v.error。
//
// 例子 2：单一类型函数（len(f.Types) == 0）
//
// 假设有函数 sqrt(float64) float64：
//
//	sqrtFunc := &builtin.Function{
//	   Name: "sqrt",
//	   Type: reflect.TypeOf(func(float64) float64 { return 0 }),
//	}
//
// 调用：
//
//	callNode := &ast.CallNode{
//	   Callee: "sqrt",
//	   Arguments: []ast.Node{ /* AST 节点代表 float64 */ },
//	}
//
// nature := checker.checkFunction(sqrtFunc, callNode, callNode.Arguments)
//
// 流程：
// ∙ f.Types == 0 → 使用 checkArguments 检查参数类型是否匹配。
// ∙ 匹配成功 → 返回 Nature{Type: float64}。
// ∙ 匹配失败 → 返回 unknown。
//
// 例子 3：多重重载函数（len(f.Types) > 0）
//
// 假设有一个函数 add，支持不同类型的重载：
//
//	addFunc := &builtin.Function{
//	   Name: "add",
//	   Types: []reflect.Type{
//	       reflect.TypeOf(func(int, int) int { return 0 }),
//	       reflect.TypeOf(func(float64, float64) float64 { return 0 }),
//	   },
//	}
//
// 调用：
//
//	callNode := &ast.CallNode{
//	   Callee: "add",
//	   Arguments: []ast.Node{ /* AST 节点代表 1, 2 */ },
//	}
//
// 流程：
// ∙ 遍历 f.Types：
//   - 尝试 (int, int) → 匹配成功 → 设置 callNode.Callee.SetType → 返回 Nature{Type: int}。
//   - 如果失败 → 尝试 (float64, float64)。
//   - 都不匹配 → 返回 unknown 或报错。
//
// 例子 4：参数不匹配的错误处理
//
// 用上面的 add 函数，当调用 add("a", 10) 时：
//   - 遍历所有重载类型，都不匹配（字符串无法转换为 int 或 float）
//   - 记录最后一个错误（如 “参数类型不匹配重载 2”）
//   - 返回 unknown 类型，并将错误存储到 v.err 中

func (v *checker) checkFunction(f *builtin.Function, node ast.Node, arguments []ast.Node) Nature {
	// 如果函数定义了 Validate 回调（一个专门的验证逻辑），就用它来校验参数。
	// 某些特殊函数（如 len(x)、append(x, y)）参数规则复杂，不好用简单的类型签名描述，就交给 Validate 来判断。
	if f.Validate != nil {
		// 获取每个参数的反射类型
		args := make([]reflect.Type, len(arguments))
		for i, arg := range arguments {
			argNature := v.visit(arg)
			if isUnknown(argNature) {
				args[i] = anyType // 未知类型视为任意类型
			} else {
				args[i] = argNature.Type
			}
		}
		// 调用自定义验证逻辑
		t, err := f.Validate(args)
		if err != nil {
			return v.error(node, "%v", err)
		}
		return Nature{Type: t}
	} else if len(f.Types) == 0 {
		// f.Types 为空，表示这个函数没有重载，只有一个函数签名，直接调用 v.checkArguments 校验参数是否匹配。
		nt, err := v.checkArguments(f.Name, Nature{Type: f.Type()}, arguments, node)
		if err != nil {
			if v.err == nil {
				v.err = err
			}
			return unknown
		}
		// No type was specified, so we assume the function returns any.
		return nt
	}

	var lastErr *file.Error
	for _, t := range f.Types { // 遍历所有重载版本
		outNature, err := v.checkArguments(f.Name, Nature{Type: t}, arguments, node)
		if err != nil {
			lastErr = err
			continue
		}

		// As we found the correct function overload, we can stop the loop.
		// Also, we need to set the correct nature of the callee so compiler,
		// can correctly handle OpDeref opcode.
		//
		// 找到正确的函数重载，修正被调函数 callee 的类型。
		if callNode, ok := node.(*ast.CallNode); ok {
			callNode.Callee.SetType(t)
		}

		return outNature
	}

	// 如果没有找到匹配的重载
	if lastErr != nil {
		if v.err == nil {
			v.err = lastErr
		}
		return unknown
	}

	return v.error(node, "no matching overload for %v", f.Name)
}

// 为什么未知类型 unknown 不会报错？
//
// 1. “未知类型” 是编译 / 解析过程中的临时状态
//	在编译器或静态分析工具的工作流程中，类型检查通常是多轮次、渐进式的：
//
//	第一次解析代码时，某些表达式的类型可能暂时无法确定（例如：变量声明和使用顺序颠倒、循环依赖的类型定义、尚未解析的泛型参数等）。
//	这种 “未知类型”（unknown）并非最终状态，可能在后续的检查轮次中被补全为具体类型。
//
// 2. 避免 “连锁错误爆炸”
//	静态类型检查中，一个基础错误可能导致后续一系列 “衍生错误”，
//	例如：如果变量 a 的类型未知，那么所有使用 a 的地方（a + 1、f(a) 等）都会间接产生类型未知。
//	若对 “未知类型” 直接报错，会出现大量重复的、无意义的错误信息（根源都是 a 的类型未确定），反而掩盖了真正的问题。
//	暂时容忍 “未知类型”，不立即报错，而是继续检查其他部分。当所有解析和检查完成后，若仍有 “未知类型” 未被补全，再集中报错（通常报 “未定义” 或 “类型无法推断”）。
//
//
// 几种常见情况。
//
//	变量未初始化或来源不明确
//		var x
//		x = someFunc()
//	如果 x 没有显式类型，且右侧表达式类型暂时无法推导，类型检查器就会标记 x 为 unknown。
//	等后续分析到 someFunc() 的返回类型时，才会确定 x 的真实类型。
//
//	函数返回值类型尚未确定
//	y := getValue()  // 编译器还没分析 getValue 的返回值类型
//	func getValue() any {
//    	return 42
//	}
//	在类型检查初期，getValue() 的返回类型可能暂时未知 → 标记为 unknown ，后续推导或类型约束会更新为 int。
//
//	表达式复杂或递归
//	var z = f(g(h(x)))
//	如果 h(x)、g(...) 或 f(...) 的类型依赖尚未推导出来，z 的类型就是 unknown。
//	类型检查器允许 unknown 先“通过”，然后再递归分析，保证分析流程不中断。
//
//	第三方或者动态类型
//
//	对某些动态语言风格或者 interface{} 类型的值：
//		var v interface{}
//		call(v)
//	v 的实际类型可能在运行时才能确定，静态阶段标记为 unknown，用于类型推迟检查。

// 验证函数调用的参数是否与函数定义匹配，包括：
//   - 参数数量是否正确（过多 / 过少都会报错）
//   - 参数类型是否与函数参数类型兼容（在需要时进行类型转换）
//   - 处理可变参数（variadic）和方法接收器（method receiver）的特殊情况
func (v *checker) checkArguments(
	name string, // 函数名
	fn Nature, // 函数类型信息
	arguments []ast.Node, // 调用时传入的参数 AST 节点列表
	node ast.Node, // 函数调用的 AST 节点
) (
	Nature, // 函数返回值类型
	*file.Error, // 参数检查失败时返回错误信息
) {

	// 如果函数类型未知，直接返回 unknown。
	if isUnknown(fn) {
		return unknown, nil
	}

	// 函数返回值必须 1 或 2 个。
	if fn.NumOut() == 0 {
		return unknown, &file.Error{
			Location: node.Location(),
			Message:  fmt.Sprintf("func %v doesn't return value", name),
		}
	}
	if numOut := fn.NumOut(); numOut > 2 {
		return unknown, &file.Error{
			Location: node.Location(),
			Message:  fmt.Sprintf("func %v returns more then two values", name),
		}
	}

	// If func is method on an env, first argument should be a receiver,
	// and actual arguments less than fnNumIn by one.
	fnNumIn := fn.NumIn()
	// 如果函数是方法，第一个参数是 receiver，入参数目需要减去 1 ；
	if fn.Method { // TODO: Move subtraction to the Nature.NumIn() and Nature.In() methods.
		fnNumIn--
	}

	// Skip first argument in case of the receiver.
	fnInOffset := 0
	// 如果函数是方法，设置 fnInOffset = 1 ，用于在访问参数列表时跳过 receiver。
	if fn.Method {
		fnInOffset = 1 // 参数索引偏移量
	}

	// 检查参数个数：
	//  - 可变参数函数：参数个数 ≥ 固定参数个数。
	//  - 普通函数：参数个数必须 == fnNumIn。
	// 如果不满足，生成 file.Error。
	var err *file.Error
	if fn.IsVariadic() { // 可变参数函数
		if len(arguments) < fnNumIn-1 { // 至少需要 n-1 个参数
			err = &file.Error{
				Location: node.Location(),
				Message:  fmt.Sprintf("not enough arguments to call %v", name),
			}
		}
	} else { // 固定参数函数
		if len(arguments) > fnNumIn { // 参数数量必须精确匹配
			err = &file.Error{
				Location: node.Location(),
				Message:  fmt.Sprintf("too many arguments to call %v", name),
			}
		}
		if len(arguments) < fnNumIn {
			err = &file.Error{
				Location: node.Location(),
				Message:  fmt.Sprintf("not enough arguments to call %v", name),
			}
		}
	}

	// 即使参数数量不对，也遍历每个参数做类型检查，方便后续修复错误。
	if err != nil {
		// If we have an error, we should still visit all arguments to
		// type check them, as a patch can fix the error later.
		for _, arg := range arguments {
			_ = v.visit(arg)
		}
		return fn.Out(0), err
	}

	// 参数类型检查
	//  - 遍历每个参数，获取其 AST 类型。
	//  - 可变参数处理：取底层元素类型（Go 中 func(xs ...int) 对应 []int）。
	//  - 方法函数偏移：跳过 receiver。
	for i, arg := range arguments {
		// 获取参数的类型信息
		argNature := v.visit(arg)

		// 确定函数期望的参数类型（处理可变参数的特殊情况）
		var in Nature
		if fn.IsVariadic() && i >= fnNumIn-1 {
			// For variadic arguments fn(xs ...int), go replaces type of xs (int) with ([]int).
			// As we compare arguments one by one, we need underling type.
			in = fn.In(fn.NumIn() - 1).Elem() // 可变参数的实际类型是切片元素类型（如 ...int 实际接收 int 类型）
		} else {
			in = fn.In(i + fnInOffset) // 获取对应位置的参数类型
		}

		// 情况1：浮点数参数接收整数（自动转换）
		if isFloat(in) && isInteger(argNature) {
			traverseAndReplaceIntegerNodesWithFloatNodes(&arguments[i], in) // 替换为浮点数节点
			continue
		}

		// 情况2：整数类型不匹配（如 int8 传给 int32，自动转换）
		if isInteger(in) && isInteger(argNature) && argNature.Kind() != in.Kind() {
			traverseAndReplaceIntegerNodesWithIntegerNodes(&arguments[i], in) // 替换为目标整数类型
			continue
		}

		// 情况3：nil 参数只能传给指针或接口类型
		if isNil(argNature) {
			if in.Kind() == reflect.Ptr || // 直接赋值
				in.Kind() == reflect.Interface { // 解引用后赋值
				continue // nil 可以赋值给指针或接口
			}
			return unknown, &file.Error{
				Location: arg.Location(),
				Message:  fmt.Sprintf("cannot use nil as argument (type %s) to call %v", in, name),
			}
		}

		// Check if argument is assignable to the function input type.
		// We check original type (like *time.Time), not dereferenced type,
		// as function input type can be pointer to a struct.
		//
		// 检查参数类型 argNature 是否可以直接赋值给函数输入类型；
		// 这里用原始类型，不解引用，因为函数可能期望一个指针类型（*time.Time），如果直接解引用就不对了。
		assignable := argNature.AssignableTo(in)

		// We also need to check if dereference arg type is assignable to the function input type.
		// For example, func(int) and argument *int. In this case we will add OpDeref to the argument,
		// so we can call the function with *int argument.
		//
		// 如果上一步匹配失败，再尝试将参数类型 “解引用”后，检查是否能赋值给函数要求的类型。
		// 例如函数期望 int，参数是 *int → 可以通过解引用传入；
		// 如果成立，就在 AST 中插入一个 OpDeref 操作，让编译器在运行时自动解引用。
		assignable = assignable || argNature.Deref().AssignableTo(in)

		// 如果参数既不可直接赋值，也不可解引用后赋值，且类型不是未知类型，就报错，返回 unknown 类型，防止后续推导错误。
		if !assignable && !isUnknown(argNature) {
			return unknown, &file.Error{
				Location: arg.Location(),
				Message:  fmt.Sprintf("cannot use %s as argument (type %s) to call %v ", argNature, in, name),
			}
		}
	}

	return fn.Out(0), nil
}

// 遍历语法树（AST）将整数节点（IntegerNode）替换为浮点数节点（FloatNode）。
//
// 只处理了三种类型节点：整数节点、一元运算节点和特定运算符的二元运算节点，对于其他类型节点不做任何处理；
// 注意，转换是不可逆的，语法树中原始整数节点会被永久替换为浮点节点。
func traverseAndReplaceIntegerNodesWithFloatNodes(node *ast.Node, newNature Nature) {
	switch (*node).(type) {
	case *ast.IntegerNode:
		// 如果是整数节点：
		// 	- 先取出整数值，转成 float64，新建一个 FloatNode。
		// 	- 再用 *node = ... 覆盖原来的节点（完成替换）。
		// 	- 最后给新建的 FloatNode 设置类型（来自 newNature.Type）。
		*node = &ast.FloatNode{Value: float64((*node).(*ast.IntegerNode).Value)}
		(*node).SetType(newNature.Type)
	case *ast.UnaryNode:
		// 如果是 一元运算节点（如 -x）：
		//	- 递归进入 UnaryNode.Node（它的子节点），继续替换。
		unaryNode := (*node).(*ast.UnaryNode)
		traverseAndReplaceIntegerNodesWithFloatNodes(&unaryNode.Node, newNature)
	case *ast.BinaryNode:
		// 如果是二元运算节点（如 x+y、x*y）：
		//	- 对 + - * 这几种运算做处理（可能因为 / 的语义在整数和浮点数里不一样，需要特殊对待）。
		//	- 递归进入左右子节点，继续替换。
		binaryNode := (*node).(*ast.BinaryNode)
		switch binaryNode.Operator {
		case "+", "-", "*":
			traverseAndReplaceIntegerNodesWithFloatNodes(&binaryNode.Left, newNature)
			traverseAndReplaceIntegerNodesWithFloatNodes(&binaryNode.Right, newNature)
		}
	}
}

// 遍历 AST，它不把 IntegerNode 替换成 FloatNode，而是对 IntegerNode / UnaryNode / BinaryNode 做类型标记更新。
func traverseAndReplaceIntegerNodesWithIntegerNodes(node *ast.Node, newNature Nature) {
	switch (*node).(type) {
	case *ast.IntegerNode: // 如果是整数节点，直接更新它的类型为对应整数反射类型。
		(*node).SetType(newNature.Type)
	case *ast.UnaryNode: // 如果是一元运算节点（如 -x、+x）：先把它本身的 Type 更新为对应整数反射类型；然后递归进入它的子节点，继续更新类型。
		(*node).SetType(newNature.Type)
		unaryNode := (*node).(*ast.UnaryNode)
		traverseAndReplaceIntegerNodesWithIntegerNodes(&unaryNode.Node, newNature)
	case *ast.BinaryNode: //
		// TODO: Binary node return type is dependent on the type of the operands. We can't just change the type of the node.
		// TODO: 二元节点的返回类型依赖于操作数类型，不能简单的直接设置，而是通过修改其操作数类型间接实现
		binaryNode := (*node).(*ast.BinaryNode)
		switch binaryNode.Operator {
		case "+", "-", "*":
			traverseAndReplaceIntegerNodesWithIntegerNodes(&binaryNode.Left, newNature)
			traverseAndReplaceIntegerNodesWithIntegerNodes(&binaryNode.Right, newNature)
		}
	}
}

// PredicateNode 分析 AST 中的谓词节点，确定并返回封装其类型信息的 Nature 对象。
//
// 示例1：简单的比较谓词
//
// 有谓词表达式：
// 	x -> x > 100
//
// 在 AST 中，这个表达式被表示为一个 PredicateNode ，其中包含一个子节点（node.Node）表示 `x > 100` 这个比较表达式：
//	PredicateNode( BinaryNode(IdentifierNode("x"), ">", IntegerNode(100)) )
//
// 处理过程：
//	- v.visit(node.Node) 访问 x > 5，返回 bool 类型 Nature{Type: reflect.TypeOf(bool{})} ，因为比较表达式返回布尔值；
//	- 由于 nt 既不是未知类型也不是空类型，所以 out 切片会被添加 bool 类型：out = []reflect.Type{bool} ；
//	- 最后返回的 Nature ：
//		Nature{
//		 	Type: reflect.FuncOf([]reflect.Type{anyType}, []reflect.Type{boolType}, false), // 即 func(interface{}) bool
//		 	PredicateOut: &Nature{Type: boolType} // bool 类型
//		}
// 结果：
//	- 这个谓词被识别为接受任意参数返回布尔值的函数
//
//
// 示例2：类型未知的谓词
//
//	原始表达式：x => someUnknownFunction(x)
//  AST结构：PredicateNode( CallNode("someUnknownFunction", IdentifierNode("x")) )
//
//	处理过程：
//		- v.visit(node.Node) 访问函数调用，返回未知类型
//		- nt = 未知类型
//		- out = []reflect.Type{anyType} （因为 isUnknown(nt)）
//
//	返回的 Nature:
//		- Type: func(interface{}) interface{}
//		- PredicateOut: 指向未知类型的指针
//
//	结果：保守地返回最通用的函数类型
//
//
// 示例3：返回nil的谓词
//
//	原始表达式：x => nil
//	AST结构：PredicateNode( NilNode() )
//	处理过程：
//		- v.visit(node.Node) 访问nil节点，返回nil类型
//		- nt = nil类型
//		- out = []reflect.Type{} （空数组，因为 isNil(nt)）
//	返回的 Nature:
//		- Type: func(interface{}) （无返回值）
//		- PredicateOut: 指向 nil 类型的指针
//	结果：创建了一个没有返回值的函数类型
//
// 示例4：复杂表达式谓词
//
//	原始表达式：person => person.age > 18 && person.name != ""
//	AST结构：PredicateNode( BinaryNode(&&, BinaryNode(>, FieldNode, IntNode), BinaryNode(!=, FieldNode, StringNode)) )
//	处理过程：
//		- v.visit(node.Node) 访问整个逻辑表达式，返回 bool 类型
//		- nt = bool 类型
//		- out = []reflect.Type{bool}
//	返回的 Nature:
//		- Type: func(interface{}) bool
//		- PredicateOut: 指向 bool 类型的指针

func (v *checker) PredicateNode(node *ast.PredicateNode) Nature {
	// 获取子节点的类型信息
	nt := v.visit(node.Node)
	// 存储谓词函数的返回类型列表
	var out []reflect.Type
	// 据 nt 的情况决定函数的返回类型：
	//	- 如果 nt 未知 → 结果是 anyType（万能类型，等价于 interface{}）。
	//	- 如果 nt 非 nil → 结果就是 nt.Type。
	//	- 如果 nt 是 nil → 那就不往 out 里加东西。
	if isUnknown(nt) {
		out = append(out, anyType)
	} else if !isNil(nt) {
		out = append(out, nt.Type)
	}

	// reflect.FuncOf 用于创建函数类型，参数分别为：输入参数类型切片、返回值类型切片、是否为可变参数
	// reflect.FuncOf([]reflect.Type{anyType}, out, false) 表示：
	//	- 参数：1 个 anyType 类型的参数
	//	- 返回值：根据 out 数组确定，对于谓词函数来说只有 0 或 1 个返回值。
	//	- 可变参数：false
	// 即：
	//	- func(anyType) out[0]

	return Nature{
		Type:         reflect.FuncOf([]reflect.Type{anyType}, out, false),
		PredicateOut: &nt,
	}
}

// 示例 1：合法的数组元素访问（无名称指针）
//
//	假设我们有这样的谓词表达式：[1,2,3,4] -> # > 2（意思是 "从数组中筛选出大于 2 的元素"）
//
//	在这个场景中：
//		PointerNode 对应的是表达式中的 #（无名称指针，代表数组当前元素）
//	处理过程：
//		检查到 v.predicateScopes 不为空（在谓词内部），获取当前作用域 scope
//		node.Name 为空，进入无名称指针处理逻辑
//		scope.collection 是 []int（整数切片类型）
//		检查到 scope.collection.Kind() 是 reflect.Slice
//		返回 scope.collection.Elem() ，即 int 类型（切片的元素类型）
//	结果：
//		类型检查通过，# 被判定为 int 类型，与2（整数）的比较合法
//
// 示例 2：合法的变量访问（有名称指针）
//	假设我们有这样的谓词表达式：users -> #age > 18（意思是 "从用户集合中筛选出年龄大于 18 的用户"）
//
//	在这个场景中：
//		PointerNode 对应的是表达式中的 #age（有名称指针，访问用户的 age 属性）
//	处理过程：
//		确认在谓词内部，获取当前作用域 scope
//		node.Name 为 "age"，进入有名称指针处理逻辑
//		scope.vars 中存在 age: int（假设用户的年龄字段是整数类型）
//		从 scope.vars 中找到 "age" 对应的类型 int 并返回
//	结果：
//		类型检查通过，#age 被判定为 int 类型，与 18 的比较合法
//
// 示例 3：错误场景 1 - 指针在谓词外使用
//
//	假设我们有这样的表达式：#x + 5（不在任何谓词内部）
//
//	处理过程：
//		检查到 v.predicateScopes 为空（没有谓词作用域）
//		直接返回错误：cannot use pointer accessor outside predicate
//	结果：
//		类型检查失败，提示指针不能在谓词外部使用
//
// 示例 4：错误场景 2 - 对非数组 / 切片使用无名称指针
//
//	假设我们有这样的谓词表达式：{"name": "Alice", "age": 20} -> # > 18（对结构体使用#）
//
//	处理过程：
//		在谓词内部，获取作用域 scope
//		node.Name 为空，处理无名称指针
//		scope.collection 是结构体类型（非数组 / 切片）
//		检查到 scope.collection.Kind() 不是 reflect.Array 或 reflect.Slice
//	返回错误：
//		cannot use struct as array
//	结果：
//		类型检查失败，提示不能对结构体使用元素访问指针#
//
// 示例 5：错误场景 3 - 访问未定义的指针变量
//	假设我们有这样的谓词表达式：users -> #salary > 5000（假设users集合中没有salary字段）
//
//	处理过程：
//		在谓词内部，获取作用域scope
//		node.Name 为 "salary"，查找 scope.vars
//		scope.vars 中没有 "salary" 的定义（ok 为 false）
//	返回错误：
//		unknown pointer #salary
//	结果：
//		类型检查失败，提示指针变量未定义

func (v *checker) PointerNode(node *ast.PointerNode) Nature {
	// 不能在谓词作用域之外使用指针，换句话说，PointerNode 只能在 PredicateNode 的上下文里用。
	if len(v.predicateScopes) == 0 {
		return v.error(node, "cannot use pointer accessor outside predicate")
	}
	// 取当前作用域（栈顶作用域），每个作用域 scope 里一般会保存：
	//	- collection：谓词所作用的集合类型（如 []int、[]User）。
	//	- vars：当前谓词可访问的变量信息（变量名 → 变量类型）。
	scope := v.predicateScopes[len(v.predicateScopes)-1]

	// 如果 PointerNode 没有名字（Name == ""），表示要取集合元素本身。
	//	- 若集合类型未知 → 返回 unknown。
	//	- 若集合是 数组/切片 类型 → 返回元素类型（Elem()）。
	//	- 若集合不是数组 / 切片（如结构体、map），返回错误。
	if node.Name == "" {
		if isUnknown(scope.collection) {
			return unknown
		}
		switch scope.collection.Kind() {
		case reflect.Array, reflect.Slice:
			return scope.collection.Elem()
		}
		return v.error(node, "cannot use %v as array", scope)
	}
	// 如果 PointerNode 有名字（例如 #id），表示是变量访问，要去 scope.vars 里查找。
	// 如果找到了，就返回这个变量对应的类型，否则报错 "未知指针" 。
	if scope.vars != nil {
		if t, ok := scope.vars[node.Name]; ok {
			return t
		}
	}
	return v.error(node, "unknown pointer #%v", node.Name)
}

// VariableDeclaratorNode 用于在编译期验证变量声明的合法性，并确定声明表达式的类型。
//
// 对应语法：let 变量名 = 初始值; 后续表达式
func (v *checker) VariableDeclaratorNode(node *ast.VariableDeclaratorNode) Nature {
	// 1. 对变量名 `node.Name` 进行重名检查

	// 检查是否与环境变量重名
	if _, ok := v.config.Env.Get(node.Name); ok {
		return v.error(node, "cannot redeclare %v", node.Name)
	}
	// 检查是否与已定义函数重名
	if _, ok := v.config.Functions[node.Name]; ok {
		return v.error(node, "cannot redeclare function %v", node.Name)
	}
	// 检查是否与内置变量/函数重名
	if _, ok := v.config.Builtins[node.Name]; ok {
		return v.error(node, "cannot redeclare builtin %v", node.Name)
	}
	// 检查是否与当前作用域中已声明的变量重名
	if _, ok := v.lookupVariable(node.Name); ok {
		return v.error(node, "cannot redeclare variable %v", node.Name)
	}

	// 2. 推导变量初始值 `node.Value` 的类型信息
	varNature := v.visit(node.Value)

	// 3. 临时添加变量到新作用域，让后续表达式 node.Expr 能访问到这个新变量
	v.varScopes = append(v.varScopes, varScope{node.Name, varNature})

	// 4. 推导表达式 `node.Expr` 的类型信息
	exprNature := v.visit(node.Expr)

	// 5. 移除变量作用域，恢复作用域状态。
	v.varScopes = v.varScopes[:len(v.varScopes)-1]

	// 6. 返回表达式类型
	return exprNature
}

func (v *checker) SequenceNode(node *ast.SequenceNode) Nature {
	if len(node.Nodes) == 0 {
		return v.error(node, "empty sequence expression")
	}
	var last Nature
	for _, node := range node.Nodes {
		last = v.visit(node)
	}
	return last
}

// lookupVariable 根据变量名查找变量作用域。
//
// 返回值：
//   - varScope：找到的变量作用域信息（如果有）。
//   - bool：是否找到该变量。
//
// v.varScopes 是一个栈（slice），存储了当前所有变量的作用域。
// 这里倒序遍历（从最新的作用域往外找），保证最近的定义优先（符合词法作用域规则）。
// 如果找到了同名变量，就直接返回它的作用域信息和 true ；如果没找到，返回一个空的 varScope 和 false，表示查找失败。
func (v *checker) lookupVariable(name string) (varScope, bool) {
	for i := len(v.varScopes) - 1; i >= 0; i-- {
		if v.varScopes[i].name == name {
			return v.varScopes[i], true
		}
	}
	return varScope{}, false
}

// ConditionalNode 对 cond ? expr1 : expr2 表达式进行类型检查和推导。
//
// 示例
//
//	示例1：相同类型
//	age > 18 ? "adult" : "minor"
//	t1 = string ("adult")
//	t2 = string ("minor")
//	t1.AssignableTo(t2) = true
//	返回: string
//
//	示例2：数值类型提升
//	flag ? 5 : 3.14
//	t1 = int (5)
//	t2 = float64 (3.14) // int 通常可以赋值给 float64
//	返回: float64
//
//	示例3：接口兼容
//	condition ? &User{} : nil
//	t1 = *User
//	t2 = nil
//	返回: *User
//
//	示例4：类型不兼容
//	flag ? "hello" : 42
//	t1 = string
//	t2 = int
//	t1.AssignableTo(t2) = false
//	返回: unknown
//
//	错误示例1：非布尔条件
//	5 ? "a" : "b"
//	错误: "non-bool expression (type int) used as condition"
//
//	错误示例2：完全不兼容的类型
//	condition ? "string" : []int{1, 2, 3}
//	返回: unknown
func (v *checker) ConditionalNode(node *ast.ConditionalNode) Nature {

	// 条件表达式必须是布尔类型
	c := v.visit(node.Cond)
	if !isBool(c) && !isUnknown(c) {
		return v.error(node.Cond, "non-bool expression (type %v) used as condition", c)
	}

	// 检查两个分支的类型
	t1 := v.visit(node.Exp1)
	t2 := v.visit(node.Exp2)

	// 处理 nil
	//  - 单边 nil : 如果一边是 nil 另一边不是，返回非 nil 的类型
	//  - 双边 nil : 如果两边都是 nil ，返回 nil 类型
	if isNil(t1) && !isNil(t2) {
		return t2
	}
	if !isNil(t1) && isNil(t2) {
		return t1
	}
	if isNil(t1) && isNil(t2) {
		return nilNature
	}

	// 处理非 nil
	//	- 如果类型兼容( t1 可以赋值给 t2 )，返回 t1 的类型
	if t1.AssignableTo(t2) {
		return t1
	}
	return unknown
}

// ArrayNode 检查数组字面量（如 [1, 2, 3] 或 ["a", "b", "c"]）的元素类型一致性并推导数组类型。
//
// ast.ArrayNode：表示源代码中的一个数组字面量节点，例如：
//   - [1, 2, 3]
//   - ["a", "b", "c"]
//   - [1, "a", true]
func (v *checker) ArrayNode(node *ast.ArrayNode) Nature {
	var prev Nature                // 保存上一个元素的类型
	allElementsAreSameType := true // 标记数组中所有元素是否类型一致
	for i, node := range node.Nodes {
		curr := v.visit(node)
		if i > 0 {
			if curr.Kind() != prev.Kind() {
				allElementsAreSameType = false
			}
		}
		prev = curr
	}

	// 如果所有元素类型一致，返回 arrayOf(prev)，即有具体元素类型的数组。
	//	- 如 [1, 2, 3] → []int
	//	- 如 ["a", "b"] → []string
	// 否则，返回一个通配数组。
	//	- 如 [1, "a", true] → []any
	if allElementsAreSameType {
		return arrayOf(prev)
	}
	return arrayNature
}

// MapNode 对 Map 字面量进行类型检查。
//
// ast.MapNode 表示 AST 里的 Map 字面量，比如：{"a": 1, "b": 2} ，包含一组 Pairs，每个 pair 是一个键值对。
//
// 先对每个键值对调用 visit，进入递归检查：
//   - 会先检查 key 表达式是否合法
//   - 再检查 value 表达式是否合法
//
// 但这里没有返回值（类型信息）保存下来，只是为了触发验证。
// 最后统一返回 mapNature
func (v *checker) MapNode(node *ast.MapNode) Nature {
	for _, pair := range node.Pairs {
		v.visit(pair)
	}
	return mapNature
}

// PairNode 对键值对进行类型检查
func (v *checker) PairNode(node *ast.PairNode) Nature {
	v.visit(node.Key)
	v.visit(node.Value)
	return nilNature
}
