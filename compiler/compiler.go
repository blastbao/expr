package compiler

import (
	"fmt"
	"log"
	"math"
	"reflect"
	"regexp"
	"strings"

	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/builtin"
	"github.com/expr-lang/expr/checker"
	. "github.com/expr-lang/expr/checker/nature"
	"github.com/expr-lang/expr/conf"
	"github.com/expr-lang/expr/file"
	"github.com/expr-lang/expr/parser"
	. "github.com/expr-lang/expr/vm"
	"github.com/expr-lang/expr/vm/runtime"
)

const (
	placeholder = 12345
)

func Compile(tree *parser.Tree, config *conf.Config) (program *Program, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()

	c := &compiler{
		config:         config,
		locations:      make([]file.Location, 0),
		constantsIndex: make(map[any]int),
		functionsIndex: make(map[string]int),
		debugInfo:      make(map[string]string),
	}

	c.compile(tree.Node)
	c.dump()

	if c.config != nil {
		switch c.config.Expect {
		case reflect.Int:
			c.emit(OpCast, 0)
		case reflect.Int64:
			c.emit(OpCast, 1)
		case reflect.Float64:
			c.emit(OpCast, 2)
		}
		if c.config.Optimize {
			c.optimize()
		}
	}

	var span *Span
	if len(c.spans) > 0 {
		span = c.spans[0]
	}

	program = NewProgram(
		tree.Source,
		tree.Node,
		c.locations,
		c.variables,
		c.constants,
		c.bytecode,
		c.arguments,
		c.functions,
		c.debugInfo,
		span,
	)
	return
}

type compiler struct {
	config         *conf.Config
	locations      []file.Location
	bytecode       []Opcode
	variables      int
	scopes         []scope
	constants      []any
	constantsIndex map[any]int
	functions      []Function
	functionsIndex map[string]int
	debugInfo      map[string]string
	nodes          []ast.Node
	spans          []*Span
	chains         [][]int
	arguments      []int

	compileDepth int
}

type scope struct {
	variableName string
	index        int
}

func (c *compiler) nodeParent() ast.Node {
	if len(c.nodes) > 1 {
		return c.nodes[len(c.nodes)-2]
	}
	return nil
}

// 将一条指令插入到字节码序列中
//
// 参数：
//   - loc：源代码位置信息（文件、行号、列号），用于错误报告和调试
//   - op：操作码（要执行的指令类型）
//   - arg：指令参数（通常是常量的索引或跳转偏移量）
//
// 操作：
//   - 指令写入：将操作码追加到 bytecode 切片中
//   - 位置记录：记录指令在字节码数组中的位置
//   - 参数存储：参数单独存储在 arguments 切片中，与操作码保持平行对应关系
//   - 调试信息：保留源码位置，用于生成调试信息
//   - 位置返回：返回当前指令的位置，便于后续跳转指令的回填
//
// 假设有以下指令序列：
//
//	Push 10
//	Push 20
//	Add
//
// 内存中存储形式：
//
//	bytecode:   [OpPush, OpPush, OpAdd]
//	arguments:  [1,      2,      0]           // 假设 10 在常量池索引 1 ，20 在索引 2
//	locations:  [loc1,   loc2,   loc3]        // 各指令对应的源代码位置
func (c *compiler) emitLocation(loc file.Location, op Opcode, arg int) int {
	c.bytecode = append(c.bytecode, op)    // 添加操作码
	current := len(c.bytecode)             // 获取指令地址
	c.arguments = append(c.arguments, arg) // 添加参数
	c.locations = append(c.locations, loc) // 添加源码位置
	return current                         // 返回指令地址（可以用于跳转、回填等用途）
}

func (c *compiler) emit(op Opcode, args ...int) int {
	arg := 0 // 参数默认为 0
	if len(args) > 1 {
		panic("too many arguments")
	}
	if len(args) == 1 {
		arg = args[0]
	}

	// 获取当前节点的位置信息
	var loc file.Location
	if len(c.nodes) > 0 {
		loc = c.nodes[len(c.nodes)-1].Location()
	}

	ip := c.emitLocation(loc, op, arg)
	c.logf("[EMIT] emit: op=%s, arg=%d, ip=%d, loc=%s", op, arg, ip, loc)
	return ip
}

func (c *compiler) emitPush(value any) int {
	return c.emit(OpPush, c.addConstant(value))
}

// 编译器生成的字节码中通常不会直接嵌入字符串、数字、方法等对象，而是：
//   - 将常量统一放入一个常量池（c.constants）
//   - 字节码中只引用常量池的索引（节省空间，利于共享）
//
// 有两个 Struct 特殊处理下。
// 对于一般的指针类型（比如 *runtime.Field），Go 默认用的是指针做 key，两个内容相同但不同实例的 *runtime.Field 指针是不相等的，
// 即便字段名和类型都一样，这样会导致重复插入内容相同的不同实例到常量池里。
// 这里通过 fmt.Sprintf("%v", field) 把内容提取出来作为 key ，可以避免重复插入。
//
// 对于可比较的对象，如果常量已存在，直接返回，避免重复插入；
// 对不可比较的对象，不判重，直接插入到常量池中；
func (c *compiler) addConstant(constant any) int {
	c.logf("[CONST] addConstant: constant=%T %v", constant, constant)

	indexable := true
	hash := constant
	switch reflect.TypeOf(constant).Kind() {
	case reflect.Slice, reflect.Map, reflect.Struct, reflect.Func:
		// 对于不可索引的对象，无法用 map 去重，所以每次都保存为新的常量。
		//
		// Q: 对于不可索引的类型，为什么不用 fmt.Sprintf("%v", obj) 这种方式呢？
		// A:
		//	- 复杂类型 %v 格式化可能无法保证唯一，如 map
		//  - 复杂类型 %v 格式化开销很大，不格式化也只是浪费些空间，trade off
		indexable = false
		c.logf("[CONST] Non-indexable type: %T", constant)
	}

	if field, ok := constant.(*runtime.Field); ok {
		indexable = true
		hash = fmt.Sprintf("%v", field)
		c.logf("[CONST] Special case *runtime.Field, key=%v", hash)
	}
	if method, ok := constant.(*runtime.Method); ok {
		indexable = true
		hash = fmt.Sprintf("%v", method)
		c.logf("[CONST] Special case *runtime.Method, key=%v", hash)
	}

	if indexable {
		if p, ok := c.constantsIndex[hash]; ok {
			c.logf("[CONST] Constant already exists at index %d", p)
			return p
		}
	}

	c.constants = append(c.constants, constant)
	p := len(c.constants) - 1
	if indexable {
		c.constantsIndex[hash] = p
		c.logf("[CONST] Inserted indexable constant at %d with key %v", p, hash)
	} else {
		c.logf("[CONST] Inserted non-indexable constant at %d", p)
	}

	return p
}

// emitFunction adds builtin.Function.Func to the program.functions and emits call opcode.
//
// 根据参数个数选择合适的 opcode ，生成对应的字节码指令，让虚拟机能在运行时正确地调用该函数。
//
// 高频场景（参数≤3）使用特化指令，子节码更紧凑（少一个 ArgsLen 参数），减少指令解码开销；通过 OpCallN 支持任意数量参数；
func (c *compiler) emitFunction(fn *builtin.Function, argsLen int) {
	switch argsLen {
	case 0:
		c.emit(OpCall0, c.addFunction(fn.Name, fn.Func))
	case 1:
		c.emit(OpCall1, c.addFunction(fn.Name, fn.Func))
	case 2:
		c.emit(OpCall2, c.addFunction(fn.Name, fn.Func))
	case 3:
		c.emit(OpCall3, c.addFunction(fn.Name, fn.Func))
	default:
		c.emit(OpLoadFunc, c.addFunction(fn.Name, fn.Func))
		c.emit(OpCallN, argsLen)
	}
}

// addFunction adds builtin.Function.Func to the program.functions and returns its index.
//
// 将函数 fn 注册到 compiler.functions 中，如果已经存在直接返回编号，避免重复插入。
func (c *compiler) addFunction(name string, fn Function) int {
	if fn == nil {
		panic("function is nil")
	}
	if p, ok := c.functionsIndex[name]; ok {
		c.logf("[FUNCTION] Reuse function: name=%q, index=%d", name, p)
		return p
	}
	p := len(c.functions)
	c.functions = append(c.functions, fn)
	c.functionsIndex[name] = p
	c.debugInfo[fmt.Sprintf("func_%d", p)] = name // 记录调试信息 <func_no, func_name>
	c.logf("[FUNCTION] Add function: name=%q, index=%d, address=%p", name, p, fn)
	return p
}

func (c *compiler) patchJump(placeholder int) {
	offset := len(c.bytecode) - placeholder
	c.arguments[placeholder-1] = offset
}

func (c *compiler) calcBackwardJump(to int) int {
	return len(c.bytecode) + 1 - to
}

func (c *compiler) logf(format string, args ...interface{}) {
	indent := strings.Repeat(" ", (c.compileDepth-1)*4)
	log.Printf(indent+format, args...)
}

func (c *compiler) compile(node ast.Node) {
	c.compileDepth++
	c.logf("[COMPILE] ➜ start node=%T: %s", node, node.String())
	defer func() {
		c.logf("[COMPILE] ⇠ done  node=%T", node)
		c.compileDepth--
	}()

	c.nodes = append(c.nodes, node)
	defer func() {
		c.nodes = c.nodes[:len(c.nodes)-1]
	}()

	if c.config != nil && c.config.Profile {
		span := &Span{
			Name:       reflect.TypeOf(node).String(),
			Expression: node.String(),
		}
		if len(c.spans) > 0 {
			prev := c.spans[len(c.spans)-1]
			prev.Children = append(prev.Children, span)
		}
		c.spans = append(c.spans, span)
		defer func() {
			if len(c.spans) > 1 {
				c.spans = c.spans[:len(c.spans)-1]
			}
		}()

		c.emit(OpProfileStart, c.addConstant(span))
		defer func() {
			c.emit(OpProfileEnd, c.addConstant(span))
		}()
	}

	switch n := node.(type) {
	case *ast.NilNode:
		c.NilNode(n)
	case *ast.IdentifierNode:
		c.IdentifierNode(n)
	case *ast.IntegerNode:
		c.IntegerNode(n)
	case *ast.FloatNode:
		c.FloatNode(n)
	case *ast.BoolNode:
		c.BoolNode(n)
	case *ast.StringNode:
		c.StringNode(n)
	case *ast.ConstantNode:
		c.ConstantNode(n)
	case *ast.UnaryNode:
		c.UnaryNode(n)
	case *ast.BinaryNode:
		c.BinaryNode(n)
	case *ast.ChainNode:
		c.ChainNode(n)
	case *ast.MemberNode:
		c.MemberNode(n)
	case *ast.SliceNode:
		c.SliceNode(n)
	case *ast.CallNode:
		c.CallNode(n)
	case *ast.BuiltinNode:
		c.BuiltinNode(n)
	case *ast.PredicateNode:
		c.PredicateNode(n)
	case *ast.PointerNode:
		c.PointerNode(n)
	case *ast.VariableDeclaratorNode:
		c.VariableDeclaratorNode(n)
	case *ast.SequenceNode:
		c.SequenceNode(n)
	case *ast.ConditionalNode:
		c.ConditionalNode(n)
	case *ast.ArrayNode:
		c.ArrayNode(n)
	case *ast.MapNode:
		c.MapNode(n)
	case *ast.PairNode:
		c.PairNode(n)
	default:
		panic(fmt.Sprintf("undefined node type (%T)", node))
	}
}

func (c *compiler) NilNode(_ *ast.NilNode) {
	c.emit(OpNil)
}

// IdentifierNode
//
// 根据标识符的不同来源（局部变量、特殊变量、环境字段、环境方法或常量）生成相应的加载操作码。
// 体现了编译器的 “多源绑定” 能力：标识符既可能来自本地作用域，也可能来自运行环境，还可能是静态常量。
//
// 步骤：
//  1. 检查标识符是否是局部变量（函数参数、let 声明等），生成 OpLoadVar 操作码，并传入变量索引，然后返回。
//  2. 如果标识符是特殊变量 "$env"，生成 OpLoadEnv 操作码（加载环境变量），然后返回。"$env" 代表用户传入的运行时环境，通常用于访问全局环境。
//  3. 声明一个 env 变量，类型为 Nature ，如果编译器的配置（c.config）不为空，从配置中获取 c.config.Env 并赋值给 env 。
//  4. 根据 env 的类型和标识符的不同情况，生成不同的操作码：
//     4.1 如果 env 是 map[string]interface{} 类型，先将标识符作为常量添加到常量池中，然后生成 OpLoadFast 操作码，并传入常量索引。
//     4.2 如果 env 是 struct ，尝试在 env 中查找字段，若找到会返回字段名、字段的 indexes ，然后生成 OpLoadField 操作码。
//     4.3 如果 env 是 struct ，尝试在 env 中查找方法，若找到会返回方法名、方法的 index ，然后生成 OpLoadMethod 操作码。
//     4.4 如果 env 是其它类型，把这个标识符当成常量名处理，先添加到常量池，然后生成 OpLoadConst 操作码，并传入常量索引。
//
// 表格：
//
//	| ----- | -------------------------- | -------------- | ----------------   |
//	| 优先级 | 判断条件                    | 发射指令        | 含义                 |
//	| ----- | -------------------------- | -------------- | ----------------   |
//	|   1   | 本地作用域有此变量            | `OpLoadVar`    | 从局部变量栈加载      |
//	|   2   | 是 `$env` 特殊标识符         | `OpLoadEnv`    | 加载整个运行环境对象   |
//	|   3   | `env` 是 map               | `OpLoadFast`   | 直接通过 key 加载     |
//	|   4   | `env` 是 struct 且匹配字段   | `OpLoadField`  | 反射加载字段          |
//	|   5   | `env` 是 struct 且匹配方法   | `OpLoadMethod` | 反射加载方法         |
//	|   6   | 全都不匹配，回退为字符串常量    | `OpLoadConst`  | 当成普通字符串常量处理 |
//	| ----- | -------------------------- | -------------- | ----------------   |
func (c *compiler) IdentifierNode(node *ast.IdentifierNode) {
	if index, ok := c.lookupVariable(node.Value); ok {
		c.emit(OpLoadVar, index)
		return
	}
	if node.Value == "$env" {
		c.emit(OpLoadEnv)
		return
	}

	var env Nature
	if c.config != nil {
		env = c.config.Env
	}

	if env.IsFastMap() {
		c.emit(OpLoadFast, c.addConstant(node.Value))
	} else if ok, index, name := checker.FieldIndex(env, node); ok {
		c.emit(OpLoadField, c.addConstant(&runtime.Field{
			Index: index,
			Path:  []string{name},
		}))
	} else if ok, index, name := checker.MethodIndex(env, node); ok {
		c.emit(OpLoadMethod, c.addConstant(&runtime.Method{
			Name:  name,
			Index: index,
		}))
	} else {
		c.emit(OpLoadConst, c.addConstant(node.Value))
	}
}

// IntegerNode
// 根据整数节点的类型和值生成相应的操作码（OpCode）
//
// 如果节点没有指定类型，直接按原始值处理，否则按目标类型转换、范围检查后再输出；
//
//	[类型分类]	[检查规则]					[说明]
//	有符号整数	int, int8, int16, ...		检查上/下限
//	无符号整数	uint, uint8, ...			需判断非负 + 上限
//	浮点数		float32, float64			直接转换（无上限检查）
//	未指定		nil 或未知类型				不转换，直接存原始 int
func (c *compiler) IntegerNode(node *ast.IntegerNode) {
	t := node.Type()
	c.logf("[COMPILE] IntegerNode type is %v", t)

	if t == nil {
		c.emitPush(node.Value)
		return
	}

	switch t.Kind() {
	case reflect.Float32:
		c.emitPush(float32(node.Value))
	case reflect.Float64:
		c.emitPush(float64(node.Value))
	case reflect.Int:
		c.emitPush(node.Value)
	case reflect.Int8:
		if node.Value > math.MaxInt8 || node.Value < math.MinInt8 {
			panic(fmt.Sprintf("constant %d overflows int8", node.Value))
		}
		c.emitPush(int8(node.Value))
	case reflect.Int16:
		if node.Value > math.MaxInt16 || node.Value < math.MinInt16 {
			panic(fmt.Sprintf("constant %d overflows int16", node.Value))
		}
		c.emitPush(int16(node.Value))
	case reflect.Int32:
		if node.Value > math.MaxInt32 || node.Value < math.MinInt32 {
			panic(fmt.Sprintf("constant %d overflows int32", node.Value))
		}
		c.emitPush(int32(node.Value))
	case reflect.Int64:
		c.emitPush(int64(node.Value))
	case reflect.Uint:
		if node.Value < 0 {
			panic(fmt.Sprintf("constant %d overflows uint", node.Value))
		}
		c.emitPush(uint(node.Value))
	case reflect.Uint8:
		if node.Value > math.MaxUint8 || node.Value < 0 {
			panic(fmt.Sprintf("constant %d overflows uint8", node.Value))
		}
		c.emitPush(uint8(node.Value))
	case reflect.Uint16:
		if node.Value > math.MaxUint16 || node.Value < 0 {
			panic(fmt.Sprintf("constant %d overflows uint16", node.Value))
		}
		c.emitPush(uint16(node.Value))
	case reflect.Uint32:
		if node.Value < 0 {
			panic(fmt.Sprintf("constant %d overflows uint32", node.Value))
		}
		c.emitPush(uint32(node.Value))
	case reflect.Uint64:
		if node.Value < 0 {
			panic(fmt.Sprintf("constant %d overflows uint64", node.Value))
		}
		c.emitPush(uint64(node.Value))
	default:
		c.emitPush(node.Value)
	}
}

func (c *compiler) FloatNode(node *ast.FloatNode) {
	switch node.Type().Kind() {
	case reflect.Float32:
		c.emitPush(float32(node.Value))
	case reflect.Float64:
		c.emitPush(node.Value)
	default:
		c.emitPush(node.Value)
	}
}

func (c *compiler) BoolNode(node *ast.BoolNode) {
	if node.Value {
		c.emit(OpTrue)
	} else {
		c.emit(OpFalse)
	}
}

func (c *compiler) StringNode(node *ast.StringNode) {
	c.emitPush(node.Value)
}

func (c *compiler) ConstantNode(node *ast.ConstantNode) {
	if node.Value == nil {
		c.emit(OpNil)
		return
	}
	c.emitPush(node.Value)
}

// UnaryNode 将表达式中的一元运算（如 -a, !b, +c）转成对应的字节码指令，行为如下：
//   - 递归编译其操作数（node.Node）
//   - 发射一元操作指令
//
// Q: 这里为什么要 deref ?
//
// 假设有结构定义：
//
//	type Env struct {
//		A *int          // 指针
//	}
//
// 例一：
//
//	expr: -A
//
// 流程：
//   - compile(A) → 加载变量 A → 得到 *int（指针）
//   - derefInNeeded(A) → 发现 A 是 *int，需要解引用 → emit OpDeref
//   - 发射 OpNegate
//
// 字节码：
//   - OpLoadField("A")
//   - OpDeref
//   - OpNegate
func (c *compiler) UnaryNode(node *ast.UnaryNode) {
	c.compile(node.Node)
	c.derefInNeeded(node.Node)

	switch node.Operator {
	case "!", "not":
		c.emit(OpNot)
	case "+":
		// Do nothing
	case "-":
		c.emit(OpNegate)
	default:
		panic(fmt.Sprintf("unknown operator (%v)", node.Operator))
	}
}

// BinaryNode 将表达式中的二元运算转成对应的字节码指令
//
// 二元运算：
//   - 比较（==, !=, >, <, >=, <=）
//   - 算术（+, -, *, /, %, **）
//   - 逻辑运算（and/or/??）
//   - 模式匹配、包含等特殊操作
//
// 编译过程：
//   - 递归编译左、右表达式
//   - 判断是否需要解引用
//   - 发射特定字节码指令
//
// 在布尔逻辑中，表达式 a || b 的值由以下规则决定：
//   - 如果 a 为真，就不再计算 b，直接返回 a 的结果
//   - 如果 a 为假，才计算 b 并返回其结果
//
// 这种行为叫做 “短路” 。
func (c *compiler) BinaryNode(node *ast.BinaryNode) {
	switch node.Operator {
	case "==":
		c.equalBinaryNode(node)

	case "!=":
		c.equalBinaryNode(node)
		c.emit(OpNot)

	case "or", "||":
		// 编译左子式
		c.compile(node.Left)
		c.derefInNeeded(node.Left)
		// OpJumpIfTrue：条件跳转指令
		//	检查栈顶值：
		//	 - 如果为 true，跳转到 end（短路求值，直接返回 true）。
		//	 - 如果为 false，继续执行。
		// placeholder：跳转地址，暂时未知，稍后通过 patchJump 修补。
		end := c.emit(OpJumpIfTrue, placeholder)
		// 只有左子式为 false 时才会执行到这里，此时左子式值已然不重要，直接弹出
		c.emit(OpPop)
		// 编译右子式
		c.compile(node.Right)
		c.derefInNeeded(node.Right)
		// 将之前 OpJumpIfTrue 的跳转地址修正为当前指令位置
		c.patchJump(end)
	case "and", "&&":
		c.compile(node.Left)
		c.derefInNeeded(node.Left)
		end := c.emit(OpJumpIfFalse, placeholder)
		c.emit(OpPop)
		c.compile(node.Right)
		c.derefInNeeded(node.Right)
		c.patchJump(end)

	case "<":
		c.compile(node.Left)
		c.derefInNeeded(node.Left)
		c.compile(node.Right)
		c.derefInNeeded(node.Right)
		c.emit(OpLess)

	case ">":
		c.compile(node.Left)
		c.derefInNeeded(node.Left)
		c.compile(node.Right)
		c.derefInNeeded(node.Right)
		c.emit(OpMore)

	case "<=":
		c.compile(node.Left)
		c.derefInNeeded(node.Left)
		c.compile(node.Right)
		c.derefInNeeded(node.Right)
		c.emit(OpLessOrEqual)

	case ">=":
		c.compile(node.Left)
		c.derefInNeeded(node.Left)
		c.compile(node.Right)
		c.derefInNeeded(node.Right)
		c.emit(OpMoreOrEqual)

	case "+":
		c.compile(node.Left)
		c.derefInNeeded(node.Left)
		c.compile(node.Right)
		c.derefInNeeded(node.Right)
		c.emit(OpAdd)

	case "-":
		c.compile(node.Left)
		c.derefInNeeded(node.Left)
		c.compile(node.Right)
		c.derefInNeeded(node.Right)
		c.emit(OpSubtract)

	case "*":
		c.compile(node.Left)
		c.derefInNeeded(node.Left)
		c.compile(node.Right)
		c.derefInNeeded(node.Right)
		c.emit(OpMultiply)

	case "/":
		c.compile(node.Left)
		c.derefInNeeded(node.Left)
		c.compile(node.Right)
		c.derefInNeeded(node.Right)
		c.emit(OpDivide)

	case "%":
		c.compile(node.Left)
		c.derefInNeeded(node.Left)
		c.compile(node.Right)
		c.derefInNeeded(node.Right)
		c.emit(OpModulo)

	case "**", "^":
		c.compile(node.Left)
		c.derefInNeeded(node.Left)
		c.compile(node.Right)
		c.derefInNeeded(node.Right)
		c.emit(OpExponent)

	case "in":
		c.compile(node.Left)
		c.derefInNeeded(node.Left)
		c.compile(node.Right)
		c.derefInNeeded(node.Right)
		c.emit(OpIn)

	case "matches":
		// matches 用于判断左侧字符串是否匹配右侧的正则表达式。
		//	- 当右侧是字符串常量时（如 s matches "^[a-z]+$"），在编译时编译正则表达式存入常量池，通过索引引用，避免重复开销。
		//	- 当右侧是变量或表达式时（如 s matches RegexVar），在运行时计算右侧表达式得到正则字符串，将其动态编译为正则对象后，再执行匹配。
		if str, ok := node.Right.(*ast.StringNode); ok {
			re, err := regexp.Compile(str.Value)
			if err != nil {
				panic(err)
			}
			c.compile(node.Left)
			c.derefInNeeded(node.Left)
			c.emit(OpMatchesConst, c.addConstant(re))
		} else {
			c.compile(node.Left)
			c.derefInNeeded(node.Left)
			c.compile(node.Right)
			c.derefInNeeded(node.Right)
			c.emit(OpMatches)
		}

	case "contains":
		c.compile(node.Left)
		c.derefInNeeded(node.Left)
		c.compile(node.Right)
		c.derefInNeeded(node.Right)
		c.emit(OpContains)

	case "startsWith":
		c.compile(node.Left)
		c.derefInNeeded(node.Left)
		c.compile(node.Right)
		c.derefInNeeded(node.Right)
		c.emit(OpStartsWith)

	case "endsWith":
		c.compile(node.Left)
		c.derefInNeeded(node.Left)
		c.compile(node.Right)
		c.derefInNeeded(node.Right)
		c.emit(OpEndsWith)

	case "..":
		c.compile(node.Left)
		c.derefInNeeded(node.Left)
		c.compile(node.Right)
		c.derefInNeeded(node.Right)
		c.emit(OpRange)

	case "??":
		// 同 or 类似，如果左操作数不是 nil，则返回左操作数；否则返回右操作数。
		//  - port := config.port ?? 8080
		//  - name := user.nickname ?? user.username
		//  - value := cachedValue ?? computeValue()
		c.compile(node.Left)
		c.derefInNeeded(node.Left)
		end := c.emit(OpJumpIfNotNil, placeholder)
		c.emit(OpPop)
		c.compile(node.Right)
		c.derefInNeeded(node.Right)
		c.patchJump(end)

	default:
		panic(fmt.Sprintf("unknown operator (%v)", node.Operator))
	}
}

func (c *compiler) equalBinaryNode(node *ast.BinaryNode) {
	c.compile(node.Left)
	c.derefInNeeded(node.Left)
	c.compile(node.Right)
	c.derefInNeeded(node.Right)

	l := kind(node.Left.Type())
	r := kind(node.Right.Type())

	leftIsSimple := isSimpleType(node.Left)
	rightIsSimple := isSimpleType(node.Right)
	leftAndRightAreSimple := leftIsSimple && rightIsSimple

	if l == r && l == reflect.Int && leftAndRightAreSimple {
		c.emit(OpEqualInt)
	} else if l == r && l == reflect.String && leftAndRightAreSimple {
		c.emit(OpEqualString)
	} else {
		c.emit(OpEqual)
	}
}

func isSimpleType(node ast.Node) bool {
	if node == nil {
		return false
	}
	t := node.Type()
	if t == nil {
		return false
	}
	return t.PkgPath() == ""
}

// ChainNode
//
// `?.` 是空值安全的链式访问符，例如 `user?.profile?.email`，只要任意中间节点为 nil，整个表达式就立即返回 nil，不会 panic。
// `?.` 用于避免因 nil 解引用导致的运行时错误。
//
// 示例1，普通链式调用：a?.b?.c
//
//	LOAD_VAR a           ; 1. 加载 a
//	JUMP_IF_NIL end      ; 2. 如果 a 为 nil，跳转到 end
//	GET_PROPERTY b       ; 3. 否则访问 a.b
//	JUMP_IF_NIL end      ; 4. 如果 a.b 为 nil，跳转到 end
//	GET_PROPERTY c       ; 5. 否则访问 a.b.c
//	end:                 ; 6. 链中断时跳转到这里
//	JUMP_IF_NOT_NIL exit ; 7. 如果结果非 nil，跳过 nil 推送
//	POP                  ; 8. 弹出结果
//	PUSH_NIL             ; 9. 推送 nil
//	exit:                ; 10. 结束
//
// 示例2，与 ?? 协作： a?.b?.c ?? 0 ，如果 a?.b?.c 为 nil 就返回 0 。
//
//	LOAD_VAR a           ; 1. 加载 a
//	JUMP_IF_NIL end      ; 2. 如果 a 为 nil，跳转到 end
//	GET_PROPERTY b       ; 3. 否则访问 a.b
//	JUMP_IF_NIL end      ; 4. 如果 a.b 为 nil，跳转到 end
//	GET_PROPERTY c       ; 5. 否则访问 a.b.c
//	end:                 ; 6. 链中断时跳转到这里
//	; 由 ?? 运算符处理后续逻辑（此处不生成 PUSH_NIL）
func (c *compiler) ChainNode(node *ast.ChainNode) {
	c.chains = append(c.chains, []int{})
	c.compile(node.Node)
	for _, ph := range c.chains[len(c.chains)-1] {
		c.patchJump(ph) // If chain activated jump here (got nit somewhere).
	}

	parent := c.nodeParent()
	if binary, ok := parent.(*ast.BinaryNode); ok && binary.Operator == "??" {
		// If chain is used in nil coalescing operator, we can omit
		// nil push at the end of the chain. The ?? operator will
		// handle it.
		//
		// 在 `??` 运算符中，跳过显式 nil 推送（由 ?? 处理）
	} else {
		// We need to put the nil on the stack, otherwise "typed"
		// nil will be used as a result of the chain.
		j := c.emit(OpJumpIfNotNil, placeholder)
		c.emit(OpPop)
		c.emit(OpNil)
		c.patchJump(j)
	}
	c.chains = c.chains[:len(c.chains)-1]
}

// 	user.Name
// 	user?.Address.City
//	user?.profile?.email
//	env?.config?.get("token")
//	session.data?.user?.profile?.email ?? "anonymous"
//
// 方法访问 a.b()		OpMethod + runtime.Method
// 静态字段路径优化 a.b.c	OpLoadField + 折叠索引
// 动态字段名 a[key]		OpFetch
// 可选链 a?.b			OpJumpIfNil + PushNil
// 支持链式回填			通过 c.chains 在 ChainNode 管理跳转

// MemberNode 表示 访问某个对象的字段或属性，也就是 a.b 这种表达式。
// 如果是 a.b()，可能会被视为 MethodNode。
// a?.b 是可选成员访问，对应 node.Optional = true。

// 设计思想:
// 方法/字段分离：优先识别方法调用，剩余按字段处理。
// 静态优化：合并多层静态字段访问为单次操作。
// 动态兼容：支持运行时计算的属性名（如 obj[key]）。
// 安全访问：通过 chains 机制实现可选链的短路逻辑。
//
//
// 完整示例流程
//
// 场景：嵌套静态字段 a.b.c
// 识别 a.b 和 c 均为静态字段。
// 合并为 OpLoadField 指令，元数据包含路径 ["b", "c"]。
//
// 生成字节码：
//  LOAD_VAR a
//  OpLoadField <Field{Index: [0, 0], Path: ["b", "c"]}>
//
// 场景：动态属性 a[b]
// 编译基值 a 和属性名表达式 b。
// 生成 OpFetch 指令：
//	LOAD_VAR a
//	LOAD_VAR b
//	OpFetch
//
//
// 场景：可选链，user?.Address.City
// 字节码：
// 	LOAD_VAR user
//	JUMP_IF_NIL end_chain     ; 可选链检查
//	FETCH_FIELD {Index: [1, 3], Path: ["Address", "City"]}
//	end_chain:
//	...                       ; ChainNode 处理 nil
//
//
//
//	方法调用 (obj.Method)	生成 OpMethod 指令，附带方法元信息。
//	嵌套字段 (a.b.c)			合并为单次 OpLoadField，索引和路径包含所有层级。
//	可选链 (obj?.prop)		插入 OpJumpIfNil，由 ChainNode 处理 nil 情况。
//	动态属性 (obj[prop])		编译属性名后生成 OpFetch。

func (c *compiler) MemberNode(node *ast.MemberNode) {
	// 编译器通过 env 分析用户传入的环境变量结构，用于检查是否可以静态识别出字段或方法。
	var env Nature
	if c.config != nil {
		env = c.config.Env
	}

	// 检查 node 是否 env 的成员方法
	//
	// 如果这个节点表示的是方法调用（非立即调用，而是“获取方法”），就发射 OpMethod 字节码，并返回。
	if ok, index, name := checker.MethodIndex(env, node); ok {
		c.compile(node.Node)
		c.emit(OpMethod, c.addConstant(&runtime.Method{
			Name:  name,
			Index: index,
		}))
		return
	}

	// 默认字段访问操作为 OpFetch ，走运行时动态字段访问（通过 Map/Struct 反射查找）；
	// 如果能静态确定字段路径，就优化为 OpFetchField 。
	op := OpFetch
	base := node.Node

	// 检查 node 是否是 env 的字段
	// 尝试解析完整的字段路径（字段折叠）
	ok, index, nodeName := checker.FieldIndex(env, node)
	path := []string{nodeName}

	if ok {
		// 将多层静态字段访问（如 a.b.c）合并为单次 OpLoadField。

		op = OpFetchField // 标记为静态字段访问

		// 尝试向上折叠字段路径（优化链式访问）
		for !node.Optional {

			// 非可选链时尝试嵌套优化，将多层静态字段访问（如 a.b.c）合并为单次 OpLoadField 。
			//
			// user.profile.email 会被优化为一个完整字段路径 [0,1,2]，直接生成一个 OpLoadField 指令，而不是多条逐级访问的 Fetch。

			// 处理标识符（如 `field` 在 `obj.sub.field`）
			if ident, isIdent := base.(*ast.IdentifierNode); isIdent {
				if ok, identIndex, name := checker.FieldIndex(env, ident); ok {
					index = append(identIndex, index...)   // 合并嵌套索引
					path = append([]string{name}, path...) // 合并嵌套路径
					c.emitLocation(ident.Location(), OpLoadField, c.addConstant(
						&runtime.Field{Index: index, Path: path},
					))
					return
				}
			}

			// 处理嵌套 MemberNode（如 `obj` 和 `sub` 在 `obj.sub.field`）
			if member, isMember := base.(*ast.MemberNode); isMember {
				if ok, memberIndex, name := checker.FieldIndex(env, member); ok {
					index = append(memberIndex, index...)
					path = append([]string{name}, path...)
					node = member
					base = member.Node
				} else {
					break
				}
			} else {
				break
			}
		}
	}

	// 不能静态优化，继续编译 base
	c.compile(base)

	// If the field is optional, we need to jump over the fetch operation.
	// If no ChainNode (none c.chains) is used, do not compile the optional fetch.
	//
	// 支持可选链：生成条件跳转指令，如果值是 nil 就跳过本次成员访问；
	//
	// user?.profile
	//	如果 user 是 nil，就跳过访问 profile
	//	c.chains 是配合 ChainNode 使用的跳转点集合
	if node.Optional && len(c.chains) > 0 {
		ph := c.emit(OpJumpIfNil, placeholder)
		c.chains[len(c.chains)-1] = append(c.chains[len(c.chains)-1], ph)
	}

	if op == OpFetch {
		// 动态成员名的情况：如 user[dynamicKey]
		// 编译 Property 可能是一个变量（如 key）
		// 发射 OpFetch 指令，在运行时反射查找字段
		c.compile(node.Property)
		c.emit(OpFetch)
	} else {
		// 静态字段访问
		// 使用静态确定的字段路径生成 OpFetchField，提高执行效率
		// runtime.Field 包含字段索引路径和路径名（用于调试）
		c.emitLocation(node.Location(), op, c.addConstant(
			&runtime.Field{Index: index, Path: path},
		))
	}
}

func (c *compiler) SliceNode(node *ast.SliceNode) {
	c.compile(node.Node)
	if node.To != nil {
		c.compile(node.To)
	} else {
		c.emit(OpLen)
	}
	if node.From != nil {
		c.compile(node.From)
	} else {
		c.emitPush(0)
	}
	c.emit(OpSlice)
}

func (c *compiler) CallNode(node *ast.CallNode) {
	fn := node.Callee.Type()
	if fn.Kind() == reflect.Func {

		fnInOffset := 0
		fnNumIn := fn.NumIn()

		switch callee := node.Callee.(type) {
		case *ast.MemberNode:
			if prop, ok := callee.Property.(*ast.StringNode); ok {
				if _, ok = callee.Node.Type().MethodByName(prop.Value); ok && callee.Node.Type().Kind() != reflect.Interface {
					fnInOffset = 1
					fnNumIn--
				}
			}
		case *ast.IdentifierNode:
			if t, ok := c.config.Env.MethodByName(callee.Value); ok && t.Method {
				fnInOffset = 1
				fnNumIn--
			}
		}

		for i, arg := range node.Arguments {
			c.compile(arg)

			var in reflect.Type
			if fn.IsVariadic() && i >= fnNumIn-1 {
				in = fn.In(fn.NumIn() - 1).Elem()
			} else {
				in = fn.In(i + fnInOffset)
			}

			c.derefParam(in, arg)
		}

	} else {
		for _, arg := range node.Arguments {
			c.compile(arg)
		}
	}

	if ident, ok := node.Callee.(*ast.IdentifierNode); ok {
		if c.config != nil {
			if fn, ok := c.config.Functions[ident.Value]; ok {
				c.emitFunction(fn, len(node.Arguments))
				return
			}
		}
	}
	c.compile(node.Callee)

	if c.config != nil {
		isMethod, _, _ := checker.MethodIndex(c.config.Env, node.Callee)
		if index, ok := checker.TypedFuncIndex(node.Callee.Type(), isMethod); ok {
			c.emit(OpCallTyped, index)
			return
		} else if checker.IsFastFunc(node.Callee.Type(), isMethod) {
			c.emit(OpCallFast, len(node.Arguments))
		} else {
			c.emit(OpCall, len(node.Arguments))
		}
	} else {
		c.emit(OpCall, len(node.Arguments))
	}
}

func (c *compiler) BuiltinNode(node *ast.BuiltinNode) {
	switch node.Name {
	case "all":
		c.compile(node.Arguments[0])
		c.derefInNeeded(node.Arguments[0])
		c.emit(OpBegin)
		var loopBreak int
		c.emitLoop(func() {
			c.compile(node.Arguments[1])
			loopBreak = c.emit(OpJumpIfFalse, placeholder)
			c.emit(OpPop)
		})
		c.emit(OpTrue)
		c.patchJump(loopBreak)
		c.emit(OpEnd)
		return

	case "none":
		c.compile(node.Arguments[0])
		c.derefInNeeded(node.Arguments[0])
		c.emit(OpBegin)
		var loopBreak int
		c.emitLoop(func() {
			c.compile(node.Arguments[1])
			c.emit(OpNot)
			loopBreak = c.emit(OpJumpIfFalse, placeholder)
			c.emit(OpPop)
		})
		c.emit(OpTrue)
		c.patchJump(loopBreak)
		c.emit(OpEnd)
		return

	case "any":
		c.compile(node.Arguments[0])
		c.derefInNeeded(node.Arguments[0])
		c.emit(OpBegin)
		var loopBreak int
		c.emitLoop(func() {
			c.compile(node.Arguments[1])
			loopBreak = c.emit(OpJumpIfTrue, placeholder)
			c.emit(OpPop)
		})
		c.emit(OpFalse)
		c.patchJump(loopBreak)
		c.emit(OpEnd)
		return

	case "one":
		c.compile(node.Arguments[0])
		c.derefInNeeded(node.Arguments[0])
		c.emit(OpBegin)
		c.emitLoop(func() {
			c.compile(node.Arguments[1])
			c.emitCond(func() {
				c.emit(OpIncrementCount)
			})
		})
		c.emit(OpGetCount)
		c.emitPush(1)
		c.emit(OpEqual)
		c.emit(OpEnd)
		return

	case "filter":
		c.compile(node.Arguments[0])
		c.derefInNeeded(node.Arguments[0])
		c.emit(OpBegin)
		c.emitLoop(func() {
			c.compile(node.Arguments[1])
			c.emitCond(func() {
				c.emit(OpIncrementCount)
				if node.Map != nil {
					c.compile(node.Map)
				} else {
					c.emit(OpPointer)
				}
			})
		})
		c.emit(OpGetCount)
		c.emit(OpEnd)
		c.emit(OpArray)
		return

	case "map":
		c.compile(node.Arguments[0])
		c.derefInNeeded(node.Arguments[0])
		c.emit(OpBegin)
		c.emitLoop(func() {
			c.compile(node.Arguments[1])
		})
		c.emit(OpGetLen)
		c.emit(OpEnd)
		c.emit(OpArray)
		return

	case "count":
		c.compile(node.Arguments[0])
		c.derefInNeeded(node.Arguments[0])
		c.emit(OpBegin)
		c.emitLoop(func() {
			if len(node.Arguments) == 2 {
				c.compile(node.Arguments[1])
			} else {
				c.emit(OpPointer)
			}
			c.emitCond(func() {
				c.emit(OpIncrementCount)
			})
		})
		c.emit(OpGetCount)
		c.emit(OpEnd)
		return

	case "sum":
		c.compile(node.Arguments[0])
		c.derefInNeeded(node.Arguments[0])
		c.emit(OpBegin)
		c.emit(OpInt, 0)
		c.emit(OpSetAcc)
		c.emitLoop(func() {
			if len(node.Arguments) == 2 {
				c.compile(node.Arguments[1])
			} else {
				c.emit(OpPointer)
			}
			c.emit(OpGetAcc)
			c.emit(OpAdd)
			c.emit(OpSetAcc)
		})
		c.emit(OpGetAcc)
		c.emit(OpEnd)
		return

	case "find":
		c.compile(node.Arguments[0])
		c.derefInNeeded(node.Arguments[0])
		c.emit(OpBegin)
		var loopBreak int
		c.emitLoop(func() {
			c.compile(node.Arguments[1])
			noop := c.emit(OpJumpIfFalse, placeholder)
			c.emit(OpPop)
			if node.Map != nil {
				c.compile(node.Map)
			} else {
				c.emit(OpPointer)
			}
			loopBreak = c.emit(OpJump, placeholder)
			c.patchJump(noop)
			c.emit(OpPop)
		})
		if node.Throws {
			c.emit(OpPush, c.addConstant(fmt.Errorf("reflect: slice index out of range")))
			c.emit(OpThrow)
		} else {
			c.emit(OpNil)
		}
		c.patchJump(loopBreak)
		c.emit(OpEnd)
		return

	case "findIndex":
		c.compile(node.Arguments[0])
		c.derefInNeeded(node.Arguments[0])
		c.emit(OpBegin)
		var loopBreak int
		c.emitLoop(func() {
			c.compile(node.Arguments[1])
			noop := c.emit(OpJumpIfFalse, placeholder)
			c.emit(OpPop)
			c.emit(OpGetIndex)
			loopBreak = c.emit(OpJump, placeholder)
			c.patchJump(noop)
			c.emit(OpPop)
		})
		c.emit(OpNil)
		c.patchJump(loopBreak)
		c.emit(OpEnd)
		return

	case "findLast":
		c.compile(node.Arguments[0])
		c.derefInNeeded(node.Arguments[0])
		c.emit(OpBegin)
		var loopBreak int
		c.emitLoopBackwards(func() {
			c.compile(node.Arguments[1])
			noop := c.emit(OpJumpIfFalse, placeholder)
			c.emit(OpPop)
			if node.Map != nil {
				c.compile(node.Map)
			} else {
				c.emit(OpPointer)
			}
			loopBreak = c.emit(OpJump, placeholder)
			c.patchJump(noop)
			c.emit(OpPop)
		})
		if node.Throws {
			c.emit(OpPush, c.addConstant(fmt.Errorf("reflect: slice index out of range")))
			c.emit(OpThrow)
		} else {
			c.emit(OpNil)
		}
		c.patchJump(loopBreak)
		c.emit(OpEnd)
		return

	case "findLastIndex":
		c.compile(node.Arguments[0])
		c.derefInNeeded(node.Arguments[0])
		c.emit(OpBegin)
		var loopBreak int
		c.emitLoopBackwards(func() {
			c.compile(node.Arguments[1])
			noop := c.emit(OpJumpIfFalse, placeholder)
			c.emit(OpPop)
			c.emit(OpGetIndex)
			loopBreak = c.emit(OpJump, placeholder)
			c.patchJump(noop)
			c.emit(OpPop)
		})
		c.emit(OpNil)
		c.patchJump(loopBreak)
		c.emit(OpEnd)
		return

	case "groupBy":
		c.compile(node.Arguments[0])
		c.derefInNeeded(node.Arguments[0])
		c.emit(OpBegin)
		c.emit(OpCreate, 1)
		c.emit(OpSetAcc)
		c.emitLoop(func() {
			c.compile(node.Arguments[1])
			c.emit(OpGroupBy)
		})
		c.emit(OpGetAcc)
		c.emit(OpEnd)
		return

	case "sortBy":
		c.compile(node.Arguments[0])
		c.derefInNeeded(node.Arguments[0])
		c.emit(OpBegin)
		if len(node.Arguments) == 3 {
			c.compile(node.Arguments[2])
		} else {
			c.emit(OpPush, c.addConstant("asc"))
		}
		c.emit(OpCreate, 2)
		c.emit(OpSetAcc)
		c.emitLoop(func() {
			c.compile(node.Arguments[1])
			c.emit(OpSortBy)
		})
		c.emit(OpSort)
		c.emit(OpEnd)
		return

	case "reduce":
		c.compile(node.Arguments[0])
		c.derefInNeeded(node.Arguments[0])
		c.emit(OpBegin)
		if len(node.Arguments) == 3 {
			c.compile(node.Arguments[2])
			c.derefInNeeded(node.Arguments[2])
			c.emit(OpSetAcc)
		} else {
			c.emit(OpPointer)
			c.emit(OpIncrementIndex)
			c.emit(OpSetAcc)
		}
		c.emitLoop(func() {
			c.compile(node.Arguments[1])
			c.emit(OpSetAcc)
		})
		c.emit(OpGetAcc)
		c.emit(OpEnd)
		return

	}

	if id, ok := builtin.Index[node.Name]; ok {
		f := builtin.Builtins[id]
		for i, arg := range node.Arguments {
			c.compile(arg)
			argType := arg.Type()
			if argType.Kind() == reflect.Ptr || arg.Nature().IsUnknown() {
				if f.Deref == nil {
					// By default, builtins expect arguments to be dereferenced.
					c.emit(OpDeref)
				} else {
					if f.Deref(i, argType) {
						c.emit(OpDeref)
					}
				}
			}
		}

		if f.Fast != nil {
			c.emit(OpCallBuiltin1, id)
		} else if f.Safe != nil {
			c.emit(OpPush, c.addConstant(f.Safe))
			c.emit(OpCallSafe, len(node.Arguments))
		} else if f.Func != nil {
			c.emitFunction(f, len(node.Arguments))
		}
		return
	}

	panic(fmt.Sprintf("unknown builtin %v", node.Name))
}

func (c *compiler) emitCond(body func()) {
	noop := c.emit(OpJumpIfFalse, placeholder)
	c.emit(OpPop)

	body()

	jmp := c.emit(OpJump, placeholder)
	c.patchJump(noop)
	c.emit(OpPop)
	c.patchJump(jmp)
}

func (c *compiler) emitLoop(body func()) {
	begin := len(c.bytecode)
	end := c.emit(OpJumpIfEnd, placeholder)

	body()

	c.emit(OpIncrementIndex)
	c.emit(OpJumpBackward, c.calcBackwardJump(begin))
	c.patchJump(end)
}

func (c *compiler) emitLoopBackwards(body func()) {
	c.emit(OpGetLen)
	c.emit(OpInt, 1)
	c.emit(OpSubtract)
	c.emit(OpSetIndex)
	begin := len(c.bytecode)
	c.emit(OpGetIndex)
	c.emit(OpInt, 0)
	c.emit(OpMoreOrEqual)
	end := c.emit(OpJumpIfFalse, placeholder)

	body()

	c.emit(OpDecrementIndex)
	c.emit(OpJumpBackward, c.calcBackwardJump(begin))
	c.patchJump(end)
}

func (c *compiler) PredicateNode(node *ast.PredicateNode) {
	c.compile(node.Node)
}

func (c *compiler) PointerNode(node *ast.PointerNode) {
	switch node.Name {
	case "index":
		c.emit(OpGetIndex)
	case "acc":
		c.emit(OpGetAcc)
	case "":
		c.emit(OpPointer)
	default:
		panic(fmt.Sprintf("unknown pointer %v", node.Name))
	}
}

// let x = 10; x + 5
//
// Push 10     ; 入栈初始数值
// Store 0     ; 存储到变量 0 ，即 x
// LoadVar 0   ; 读取变量 0
// Push 5      ; 入栈参数数值
// Add         ; 相加
func (c *compiler) addVariable(name string) int {
	c.variables++
	c.debugInfo[fmt.Sprintf("var_%d", c.variables-1)] = name
	return c.variables - 1
}

func (c *compiler) VariableDeclaratorNode(node *ast.VariableDeclaratorNode) {
	c.compile(node.Value)
	index := c.addVariable(node.Name)
	c.emit(OpStore, index)
	c.beginScope(node.Name, index)
	c.compile(node.Expr)
	c.endScope()
}

func (c *compiler) SequenceNode(node *ast.SequenceNode) {
	for i, n := range node.Nodes {
		c.compile(n)
		if i < len(node.Nodes)-1 {
			c.emit(OpPop)
		}
	}
}

func (c *compiler) beginScope(name string, index int) {
	c.scopes = append(c.scopes, scope{name, index})
}

func (c *compiler) endScope() {
	c.scopes = c.scopes[:len(c.scopes)-1]
}

func (c *compiler) lookupVariable(name string) (int, bool) {
	for i := len(c.scopes) - 1; i >= 0; i-- {
		if c.scopes[i].variableName == name {
			return c.scopes[i].index, true
		}
	}
	return 0, false
}

func (c *compiler) ConditionalNode(node *ast.ConditionalNode) {
	c.compile(node.Cond)
	otherwise := c.emit(OpJumpIfFalse, placeholder)

	c.emit(OpPop)
	c.compile(node.Exp1)
	end := c.emit(OpJump, placeholder)

	c.patchJump(otherwise)
	c.emit(OpPop)
	c.compile(node.Exp2)

	c.patchJump(end)
}

func (c *compiler) ArrayNode(node *ast.ArrayNode) {
	for _, node := range node.Nodes {
		c.compile(node)
	}

	c.emitPush(len(node.Nodes))
	c.emit(OpArray)
}

func (c *compiler) MapNode(node *ast.MapNode) {
	for _, pair := range node.Pairs {
		c.compile(pair)
	}

	c.emitPush(len(node.Pairs))
	c.emit(OpMap)
}

func (c *compiler) PairNode(node *ast.PairNode) {
	c.compile(node.Key)
	c.compile(node.Value)
}

func (c *compiler) derefInNeeded(node ast.Node) {
	if node.Nature().Nil {
		return
	}
	switch node.Type().Kind() {
	case reflect.Ptr, reflect.Interface:
		c.emit(OpDeref)
	}
}

func (c *compiler) derefParam(in reflect.Type, param ast.Node) {
	if param.Nature().Nil {
		return
	}
	if param.Type().AssignableTo(in) {
		return
	}
	if in.Kind() != reflect.Ptr && param.Type().Kind() == reflect.Ptr {
		c.emit(OpDeref)
	}
}

func (c *compiler) optimize() {
	for i, op := range c.bytecode {
		switch op {
		case OpJumpIfTrue, OpJumpIfFalse, OpJumpIfNil, OpJumpIfNotNil:
			target := i + c.arguments[i] + 1
			for target < len(c.bytecode) && c.bytecode[target] == op {
				target += c.arguments[target] + 1
			}
			c.arguments[i] = target - i - 1
		}
	}
}

func kind(t reflect.Type) reflect.Kind {
	if t == nil {
		return reflect.Invalid
	}
	return t.Kind()
}

func (c *compiler) dump() {
	fmt.Println("====== [COMPILER DUMP] ======")

	// 打印 Bytecode + Arguments + 源码位置信息
	for i, op := range c.bytecode {
		arg := 0
		if i < len(c.arguments) {
			arg = c.arguments[i]
		}
		loc := ""
		if i < len(c.locations) {
			loc = c.locations[i].String()
		}
		fmt.Printf("[%-3d] %-20s arg=%-5d loc=%s", i, op.String(), arg, loc)
		fmt.Println()
	}

	// 打印常量池
	fmt.Println("\n[Constants]")
	for i, v := range c.constants {
		fmt.Printf("  #%d: %T = %v", i, v, v)
		fmt.Println()
	}

	// 打印函数池
	fmt.Println("\n[Functions]")
	for i, fn := range c.functions {
		fmt.Printf("  #%d: %T at %p", i, fn, fn)
		fmt.Println()
	}

	// 打印链结构（用于 ChainNode）
	if len(c.chains) > 0 {
		fmt.Println("\n[Chains]")
		for i, chain := range c.chains {
			fmt.Printf("  Chain[%d]: %v", i, chain)
			fmt.Println()
		}
	}

	fmt.Println("====== [END DUMP] ======")
}
