package parser

import (
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"

	. "github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/builtin"
	"github.com/expr-lang/expr/conf"
	"github.com/expr-lang/expr/file"
	. "github.com/expr-lang/expr/parser/lexer"
	"github.com/expr-lang/expr/parser/operator"
	"github.com/expr-lang/expr/parser/utils"
)

type arg byte

const (
	expr arg = 1 << iota
	predicate
)

const optional arg = 1 << 7

var predicates = map[string]struct {
	args []arg
}{
	"all":           {[]arg{expr, predicate}},
	"none":          {[]arg{expr, predicate}},
	"any":           {[]arg{expr, predicate}},
	"one":           {[]arg{expr, predicate}},
	"filter":        {[]arg{expr, predicate}},
	"map":           {[]arg{expr, predicate}},
	"count":         {[]arg{expr, predicate | optional}},
	"sum":           {[]arg{expr, predicate | optional}},
	"find":          {[]arg{expr, predicate}},
	"findIndex":     {[]arg{expr, predicate}},
	"findLast":      {[]arg{expr, predicate}},
	"findLastIndex": {[]arg{expr, predicate}},
	"groupBy":       {[]arg{expr, predicate}},
	"sortBy":        {[]arg{expr, predicate, expr | optional}},
	"reduce":        {[]arg{expr, predicate, expr | optional}},
}

type parser struct {
	tokens     []Token     // 输入的 token 流
	current    Token       // 当前正在处理的 token
	pos        int         // 当前 token 的索引
	err        *file.Error // 解析错误，遇错停止
	config     *conf.Config
	depth      int  // predicate call depth
	nodeCount  uint // tracks number of AST nodes created
	parseDepth int  // 新增专用于解析日志缩进
}

// checkNodeLimit 用于防止解析树节点过多导致的资源耗尽。
func (p *parser) checkNodeLimit() error {
	p.nodeCount++
	if p.config == nil {
		if p.nodeCount > conf.DefaultMaxNodes {
			p.error("compilation failed: expression exceeds maximum allowed nodes")
			return nil
		}
		return nil
	}
	if p.config.MaxNodes > 0 && p.nodeCount > p.config.MaxNodes {
		p.error("compilation failed: expression exceeds maximum allowed nodes")
		return nil
	}
	return nil
}

func (p *parser) createNode(n Node, loc file.Location) Node {
	if err := p.checkNodeLimit(); err != nil {
		return nil
	}
	if n == nil || p.err != nil {
		return nil
	}
	n.SetLocation(loc)
	return n
}

func (p *parser) createMemberNode(n *MemberNode, loc file.Location) *MemberNode {
	if err := p.checkNodeLimit(); err != nil {
		return nil
	}
	if n == nil || p.err != nil {
		return nil
	}
	n.SetLocation(loc)
	return n
}

type Tree struct {
	Node   Node
	Source file.Source
}

func Parse(input string) (*Tree, error) {
	return ParseWithConfig(input, nil)
}

func ParseWithConfig(input string, config *conf.Config) (*Tree, error) {
	// 构造输入
	source := file.NewSource(input)

	// 词法分析
	tokens, err := Lex(source)
	if err != nil {
		return nil, err
	}

	p := &parser{
		tokens:  tokens,
		current: tokens[0],
		config:  config,
	}

	node := p.parseSequenceExpression()

	if !p.current.Is(EOF) {
		p.error("unexpected token %v", p.current)
	}

	tree := &Tree{
		Node:   node,
		Source: source,
	}

	if p.err != nil {
		return tree, p.err.Bind(source)
	}

	return tree, nil
}

func (p *parser) error(format string, args ...any) {
	p.errorAt(p.current, format, args...)
}

func (p *parser) errorAt(token Token, format string, args ...any) {
	if p.err == nil { // show first error
		p.err = &file.Error{
			Location: token.Location,
			Message:  fmt.Sprintf(format, args...),
		}
	}
}

func (p *parser) next() {
	p.pos++
	if p.pos >= len(p.tokens) {
		p.error("unexpected end of expression")
		return
	}
	p.current = p.tokens[p.pos]
}

func (p *parser) expect(kind Kind, values ...string) {
	if p.current.Is(kind, values...) {
		p.next()
		return
	}
	p.error("unexpected token %v", p.current)
}

// parse functions

func (p *parser) parseSequenceExpression() Node {
	// 解析第一个表达式
	nodes := []Node{p.parseExpression(0)}

	// 处理分号分隔的其它表达式
	for p.current.Is(Operator, ";") && p.err == nil {
		p.next()
		// If a trailing semicolon is present, break out.
		if p.current.Is(EOF) {
			break
		}
		nodes = append(nodes, p.parseExpression(0))
	}

	// 只有一个表达式，不封装 SequenceNode 直接返回
	if len(nodes) == 1 {
		return nodes[0]
	}

	return p.createNode(&SequenceNode{
		Nodes: nodes,
	}, nodes[0].Location())
}

// parseExpression 的目标就是：把一个表达式字符串（已经被词法分析成 token 列表），变成语法树结构（AST）。

func (p *parser) parseExpressionOrigin(precedence int) Node {
	if p.err != nil {
		return nil
	}

	if precedence == 0 && p.current.Is(Operator, "let") {
		return p.parseVariableDeclaration()
	}
	if precedence == 0 && p.current.Is(Operator, "if") {
		return p.parseConditionalIf()
	}

	nodeLeft := p.parsePrimary()
	prevOperator := ""
	opToken := p.current

	for opToken.Is(Operator) && p.err == nil {

		negate := opToken.Is(Operator, "not")
		var notToken Token

		// Handle "not *" operator, like "not in" or "not contains".
		if negate {
			currentPos := p.pos
			p.next()
			if operator.AllowedNegateSuffix(p.current.Value) {
				if op, ok := operator.Binary[p.current.Value]; ok && op.Precedence >= precedence {
					notToken = p.current
					opToken = p.current
				} else {
					p.pos = currentPos
					p.current = opToken
					break
				}
			} else {
				p.error("unexpected token %v", p.current)
				break
			}
		}

		if op, ok := operator.Binary[opToken.Value]; ok && op.Precedence >= precedence {
			p.next()

			if opToken.Value == "|" {
				identToken := p.current
				p.expect(Identifier)
				nodeLeft = p.parseCall(identToken, []Node{nodeLeft}, true)
				goto next
			}

			if prevOperator == "??" && opToken.Value != "??" && !opToken.Is(Bracket, "(") {
				p.errorAt(opToken, "Operator (%v) and coalesce expressions (??) cannot be mixed. Wrap either by parentheses.", opToken.Value)
				break
			}

			if operator.IsComparison(opToken.Value) {
				nodeLeft = p.parseComparison(nodeLeft, opToken, op.Precedence)
				goto next
			}

			var nodeRight Node
			if op.Associativity == operator.Left {
				nodeRight = p.parseExpression(op.Precedence + 1)
			} else {
				nodeRight = p.parseExpression(op.Precedence)
			}

			nodeLeft = p.createNode(&BinaryNode{
				Operator: opToken.Value,
				Left:     nodeLeft,
				Right:    nodeRight,
			}, opToken.Location)
			if nodeLeft == nil {
				return nil
			}

			if negate {
				nodeLeft = p.createNode(&UnaryNode{
					Operator: "not",
					Node:     nodeLeft,
				}, notToken.Location)
				if nodeLeft == nil {
					return nil
				}
			}

			goto next
		}
		break

	next:
		prevOperator = opToken.Value
		opToken = p.current
	}

	if precedence == 0 {
		nodeLeft = p.parseConditional(nodeLeft)
	}

	return nodeLeft
}

func (p *parser) parseExpression(precedence int) Node {
	p.parseDepth++
	defer func() { p.parseDepth-- }()

	p.logf("[PARSE] ParseExpress(prec=%d) at token=%v pos=%d", precedence, p.current, p.pos)

	if p.err != nil {
		p.logf("[ERROR] Abort due to existing error")
		return nil
	}

	// 特殊关键字处理
	if precedence == 0 {
		if p.current.Is(Operator, "let") {
			p.logf("[LET] Start variable declaration")
			return p.parseVariableDeclaration()
		}
		if p.current.Is(Operator, "if") {
			p.logf("[IF] Start conditional expression")
			return p.parseConditionalIf()
		}
	}

	// 简单理解，每个表达式都有左右两边。
	// 当解析到一个 operator 时，它肯定有左半部，这个就是 primary ；
	// 当继续解析 operator 的右半部时，从当前 op 到下一个 op 之间的部分，就是下一个 op 的 primary 部分。
	nodeLeft := p.parsePrimary()
	p.logf("[LEFT] Parse left node=%T(%v)", nodeLeft, nodeLeft)

	prevOperator := ""
	opToken := p.current

	// 运算符处理循环
	for opToken.Is(Operator) && p.err == nil {
		p.logf("[OP] Reach op `%v` at pos=%d", opToken.Value, p.pos)

		// 处理否定运算符
		negate := opToken.Is(Operator, "not")
		var notToken Token
		if negate {
			p.logf("[NOT] Found negation operator")
			currentPos := p.pos
			p.next()
			if operator.AllowedNegateSuffix(p.current.Value) {
				if op, ok := operator.Binary[p.current.Value]; ok && op.Precedence >= precedence {
					p.logf("[NOT] Combine with %v", p.current.Value)
					notToken = p.current
					opToken = p.current
				} else {
					p.logf("[NOT] Revert - insufficient precedence %d < %d",
						op.Precedence, precedence)
					p.pos = currentPos
					p.current = opToken
					break
				}
			} else {
				p.logf("[ERROR] Invalid negation with %v", p.current.Value)
				p.error("unexpected token %v", p.current)
				break
			}
		}

		op, ok := operator.Binary[opToken.Value]
		if ok {
			if op.Precedence >= precedence {
				p.logf("[OP] Handle binary op `%s` (prec=%d, assoc=%v)", opToken.Value, op.Precedence, op.Associativity)
				p.next()

				// 管道运算符特殊处理
				if opToken.Value == "|" {
					p.logf("[PIPE] Process pipe to %v", p.current.Value)
					identToken := p.current
					p.expect(Identifier)
					nodeLeft = p.parseCall(identToken, []Node{nodeLeft}, true)
					goto next
				}

				// 空值合并运算符限制
				if prevOperator == "??" && opToken.Value != "??" && !opToken.Is(Bracket, "(") {
					p.logf("[ERROR] Invalid mix of ?? with %v", opToken.Value)
					p.errorAt(opToken, "Operator (%v) and coalesce expressions (??) cannot be mixed", opToken.Value)
					break
				}

				// 比较运算符特殊处理
				if operator.IsComparison(opToken.Value) {
					p.logf("[COMP] Chain comparison %v", opToken.Value)
					nodeLeft = p.parseComparison(nodeLeft, opToken, op.Precedence)
					goto next
				}

				// 递归解析右侧
				var nodeRight Node
				if op.Associativity == operator.Left {
					p.logf("[OP] Parse right of `%s`, assoc=left, prec=%d", opToken.Value, op.Precedence+1)
					nodeRight = p.parseExpression(op.Precedence + 1)
				} else {
					p.logf("[OP] Parse right of `%s`, assoc=left, prec=%d", opToken.Value, op.Precedence)
					nodeRight = p.parseExpression(op.Precedence)
				}
				p.logf("[RIGHT] Parse right node=%T(%v)", nodeRight, nodeRight)

				// 构建二元运算节点
				nodeLeft = p.createNode(&BinaryNode{
					Operator: opToken.Value,
					Left:     nodeLeft,
					Right:    nodeRight,
				}, opToken.Location)
				p.logf("[OP] Build Binary Node %T: `%v` %s `%v`",
					nodeLeft,
					nodeLeft.(*BinaryNode).Left,
					nodeLeft.(*BinaryNode).Operator,
					nodeLeft.(*BinaryNode).Right)

				// 处理否定包装
				if negate {
					p.logf("[NOT] Wrap with negation")
					nodeLeft = p.createNode(&UnaryNode{
						Operator: "not",
						Node:     nodeLeft,
					}, notToken.Location)
				}
				p.logf("[OP] Finish binary op `%s`", opToken.Value)
				goto next
			} else {
				p.logf("[OP] Stop handle op `%v` because prec %d < required %d", opToken.Value, operator.Binary[opToken.Value].Precedence, precedence)
			}
		} else {
			p.logf("[OP] Stop handle op `%v` because it's not binary", opToken.Value)
		}

		break

	next:
		prevOperator = opToken.Value
		opToken = p.current
		p.logf("[PARSE] Move to next operator %v", opToken.Value)
	}

	// 条件表达式处理
	if precedence == 0 {
		p.logf("[PARSE] Check for ternary operator")
		nodeLeft = p.parseConditional(nodeLeft)
	}

	p.logf("[PARSE] Exit parseExpression, return %T(%v)", nodeLeft, nodeLeft)
	return nodeLeft
}

func (p *parser) logf(format string, args ...interface{}) {
	indent := strings.Repeat(" ", (p.parseDepth-1)*4)
	log.Printf(indent+format, args...)
}

// let 变量名 = 初始值; 后续表达式
func (p *parser) parseVariableDeclaration() Node {
	// 验证并消费 let 关键字
	p.expect(Operator, "let")
	// 获取变量名
	variableName := p.current

	// 确认当前 token 是合法标识符，跳过
	p.expect(Identifier)
	// 确认当前 token 是 = operator，跳过
	p.expect(Operator, "=")

	// 解析值表达式
	value := p.parseExpression(0)
	p.expect(Operator, ";")

	// 解析后续表达式
	node := p.parseSequenceExpression()
	return p.createNode(&VariableDeclaratorNode{
		Name:  variableName.Value,
		Value: value,
		Expr:  node,
	}, variableName.Location)
}

// 解析 if-else 表达式
//
//	if condition {
//		expr1
//	} else {
//		expr2
//	}
//
// 注意，这不是普通语言里的控制语句，而是将其翻译成一个返回值的三元表达式树结构，最终构建的是 ConditionalNode ，和 cond ? expr1 : expr2 是等价的。
func (p *parser) parseConditionalIf() Node {
	// 消费 'if'
	p.next()

	// 解析 cond 条件
	nodeCondition := p.parseExpression(0)

	// 解析 if 分支
	p.expect(Bracket, "{")
	expr1 := p.parseSequenceExpression()
	p.expect(Bracket, "}")

	// 解析 else 分支
	p.expect(Operator, "else")
	p.expect(Bracket, "{")
	expr2 := p.parseSequenceExpression()
	p.expect(Bracket, "}")

	return &ConditionalNode{
		Cond: nodeCondition,
		Exp1: expr1,
		Exp2: expr2,
	}
}

// 三目条件表达式:
//   - a?b:c
//   - a?:c
func (p *parser) parseConditional(node Node) Node {
	var expr1, expr2 Node
	// 支持嵌套条件表达式（如 a?b:c?d:e）
	for p.current.Is(Operator, "?") && p.err == nil {
		p.next() // 消耗掉问号 '?'
		if !p.current.Is(Operator, ":") {
			// 标准形式 a?b:c
			expr1 = p.parseExpression(0)
			p.expect(Operator, ":")
			expr2 = p.parseExpression(0)
		} else {
			// 简写形式 a?:c（等价于 a?a:c）
			p.next() // 消耗掉冒号 ':'
			expr1 = node
			expr2 = p.parseExpression(0)
		}

		node = p.createNode(&ConditionalNode{
			Cond: node,
			Exp1: expr1,
			Exp2: expr2,
		}, p.current.Location)
		if node == nil {
			return nil
		}
	}
	return node
}

func (p *parser) parsePrimary() Node {
	token := p.current

	if token.Is(Operator) {
		if op, ok := operator.Unary[token.Value]; ok {
			p.next()
			expr := p.parseExpression(op.Precedence)
			node := p.createNode(&UnaryNode{
				Operator: token.Value,
				Node:     expr,
			}, token.Location)
			if node == nil {
				return nil
			}
			return p.parsePostfixExpression(node)
		}
	}

	if token.Is(Bracket, "(") {
		p.next()
		expr := p.parseSequenceExpression()
		p.expect(Bracket, ")") // "an opened parenthesis is not properly closed"
		return p.parsePostfixExpression(expr)
	}

	if p.depth > 0 {
		if token.Is(Operator, "#") || token.Is(Operator, ".") {
			name := ""
			if token.Is(Operator, "#") {
				p.next()
				if p.current.Is(Identifier) {
					name = p.current.Value
					p.next()
				}
			}
			node := p.createNode(&PointerNode{Name: name}, token.Location)
			if node == nil {
				return nil
			}
			return p.parsePostfixExpression(node)
		}
	}

	if token.Is(Operator, "::") {
		p.next()
		token = p.current
		p.expect(Identifier)
		return p.parsePostfixExpression(p.parseCall(token, []Node{}, false))
	}

	return p.parseSecondary()
}

func (p *parser) parseSecondary() Node {
	var node Node
	token := p.current

	switch token.Kind {

	case Identifier:
		p.next()
		switch token.Value {
		case "true":
			node = p.createNode(&BoolNode{Value: true}, token.Location)
			if node == nil {
				return nil
			}
			return node
		case "false":
			node = p.createNode(&BoolNode{Value: false}, token.Location)
			if node == nil {
				return nil
			}
			return node
		case "nil":
			node = p.createNode(&NilNode{}, token.Location)
			if node == nil {
				return nil
			}
			return node
		default:
			if p.current.Is(Bracket, "(") {
				node = p.parseCall(token, []Node{}, true)
			} else {
				node = p.createNode(&IdentifierNode{Value: token.Value}, token.Location)
				if node == nil {
					return nil
				}
			}
		}

	case Number:
		p.next()
		value := strings.Replace(token.Value, "_", "", -1)
		var node Node
		valueLower := strings.ToLower(value)
		switch {
		case strings.HasPrefix(valueLower, "0x"):
			number, err := strconv.ParseInt(value, 0, 64)
			if err != nil {
				p.error("invalid hex literal: %v", err)
			}
			node = p.toIntegerNode(number)
		case strings.ContainsAny(valueLower, ".e"):
			number, err := strconv.ParseFloat(value, 64)
			if err != nil {
				p.error("invalid float literal: %v", err)
			}
			node = p.toFloatNode(number)
		case strings.HasPrefix(valueLower, "0b"):
			number, err := strconv.ParseInt(value, 0, 64)
			if err != nil {
				p.error("invalid binary literal: %v", err)
			}
			node = p.toIntegerNode(number)
		case strings.HasPrefix(valueLower, "0o"):
			number, err := strconv.ParseInt(value, 0, 64)
			if err != nil {
				p.error("invalid octal literal: %v", err)
			}
			node = p.toIntegerNode(number)
		default:
			number, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				p.error("invalid integer literal: %v", err)
			}
			node = p.toIntegerNode(number)
		}
		if node != nil {
			node.SetLocation(token.Location)
		}
		return node
	case String:
		p.next()
		node = p.createNode(&StringNode{Value: token.Value}, token.Location)
		if node == nil {
			return nil
		}

	default:
		if token.Is(Bracket, "[") {
			node = p.parseArrayExpression(token)
		} else if token.Is(Bracket, "{") {
			node = p.parseMapExpression(token)
		} else {
			p.error("unexpected token %v", token)
		}
	}

	return p.parsePostfixExpression(node)
}

func (p *parser) toIntegerNode(number int64) Node {
	if number > math.MaxInt {
		p.error("integer literal is too large")
		return nil
	}
	return p.createNode(&IntegerNode{Value: int(number)}, p.current.Location)
}

func (p *parser) toFloatNode(number float64) Node {
	if number > math.MaxFloat64 {
		p.error("float literal is too large")
		return nil
	}
	return p.createNode(&FloatNode{Value: number}, p.current.Location)
}

func (p *parser) parseCall(token Token, arguments []Node, checkOverrides bool) Node {
	var node Node

	isOverridden := false
	if p.config != nil {
		isOverridden = p.config.IsOverridden(token.Value)
	}
	isOverridden = isOverridden && checkOverrides

	if b, ok := predicates[token.Value]; ok && !isOverridden {
		p.expect(Bracket, "(")

		// In case of the pipe operator, the first argument is the left-hand side
		// of the operator, so we do not parse it as an argument inside brackets.
		args := b.args[len(arguments):]

		for i, arg := range args {
			if arg&optional == optional {
				if p.current.Is(Bracket, ")") {
					break
				}
			} else {
				if p.current.Is(Bracket, ")") {
					p.error("expected at least %d arguments", len(args))
				}
			}

			if i > 0 {
				p.expect(Operator, ",")
			}
			var node Node
			switch {
			case arg&expr == expr:
				node = p.parseExpression(0)
			case arg&predicate == predicate:
				node = p.parsePredicate()
			}
			arguments = append(arguments, node)
		}

		// skip last comma
		if p.current.Is(Operator, ",") {
			p.next()
		}
		p.expect(Bracket, ")")

		node = p.createNode(&BuiltinNode{
			Name:      token.Value,
			Arguments: arguments,
		}, token.Location)
		if node == nil {
			return nil
		}
	} else if _, ok := builtin.Index[token.Value]; ok && (p.config == nil || !p.config.Disabled[token.Value]) && !isOverridden {
		node = p.createNode(&BuiltinNode{
			Name:      token.Value,
			Arguments: p.parseArguments(arguments),
		}, token.Location)
		if node == nil {
			return nil
		}

	} else {
		callee := p.createNode(&IdentifierNode{Value: token.Value}, token.Location)
		if callee == nil {
			return nil
		}
		node = p.createNode(&CallNode{
			Callee:    callee,
			Arguments: p.parseArguments(arguments),
		}, token.Location)
		if node == nil {
			return nil
		}
	}
	return node
}

// parseArguments 解析函数或方法调用中的实参列表。它从输入流中逐个读取参数，并处理逗号分隔和括号匹配。
//
// 1. 记录已有参数的数量 offset ，用于判断是否需要解析 ',' 分隔符;
// 2. 要求当前 token 是左括号 '(' , 只要还没遇到 ')' 且没有语法错误，就不断解析参数;
// 3. 若已经解析了一个或多个参数，解析后续参数前必须看到逗号 ',' ，读取完 ',' 如果紧接着是 ')' ，直接跳出循环，不用等到下次 loop ;
// 4. 解析参数表达式并添加到 args 中;
// 5. 参数列表以 ')' 结尾;
//
// 例子：
//
//	f(1, x + 2, "hi")
//
//	args = [
//	  Node(IntLiteral(1)),
//	  Node(BinaryExpr(x, +, 2)),
//	  Node(StringLiteral("hi"))
//	]
func (p *parser) parseArguments(arguments []Node) []Node {
	// If pipe operator is used, the first argument is the left-hand side
	// of the operator, so we do not parse it as an argument inside brackets.
	offset := len(arguments)

	p.expect(Bracket, "(")
	for !p.current.Is(Bracket, ")") && p.err == nil {
		if len(arguments) > offset {
			p.expect(Operator, ",")
		}
		if p.current.Is(Bracket, ")") {
			break
		}
		node := p.parseExpression(0)
		arguments = append(arguments, node)
	}
	p.expect(Bracket, ")")

	return arguments
}

// 谓词（Predicate） 在编程语言和计算机科学中，指的是一个 返回布尔值（true/false）的表达式或函数，用于表示逻辑条件或状态判断。
// 它的核心作用是 对数据进行筛选、验证或控制流程。
//
// 应用场景：
//	场景			示例								作用
//	条件语句		if (predicate) { ... }			控制代码分支执行。
//	循环控制		while (predicate) { ... }		决定是否继续循环。
//	数据过滤		list.Where(predicate)			筛选集合中满足条件的元素。
//	断言/验证	assert(predicate, "error")		检查程序状态是否合法。
//
// 谓词 vs 普通表达式
//	特性			谓词					普通表达式
//	返回值		必须为 true/false	可以是任意类型
//	用途			逻辑判断				计算或生成值
//	示例			x > 0				x + 1
//
// 谓词和 bool 表达式有啥区别？
//  谓词是 逻辑层面的概念（描述“是否符合条件”）。
//	布尔表达式是 语法层面的代码片段（由运算符和变量组成）。
//	所有布尔表达式都可以当作谓词使用；但不是所有谓词都是纯布尔表达式（有的可能有副作用或是多语句）。
//
// 	概念			谓词（Predicate）						布尔表达式（Boolean Expression）
//	本质			一种返回布尔值的函数或表达式					一个计算结果为布尔类型的表达式
//	作用			用于过滤、判断、条件执行等场景				用于控制流程或条件语句
//	示例			x -> x > 10、filter { x > 10 }			x > 10、a && b、!flag
//	抽象程度		是“函数”意义上的判断逻辑					是具体计算的布尔值
//	表达形式		可以是代码块 { ... }，有时支持赋值等副作用	纯粹的表达式，无副作用

// 过程：
//
// 检查是否用花括号包裹，若是则将 withBrackets 置为 true ，例如：
//   - { age > 18 } → withBrackets = true。
//   - x > 0 → withBrackets = false。
//
// 递增 p.depth ，控制递归深度，避免栈溢出（如嵌套过深的表达式）。
// 解析表达式
//   - parseSequenceExpression：解析 {} 内的多个语句，如 { a > 1; b < 2 }。
//   - parseExpression(0)：解析单条表达式，如 x > 0，优先级为 0 ；在无花括号时，遇到分号会报错，要求强制使用 {} 包裹多条语句。
//
// 如果存在 { 必须匹配 }，否则语法错误。
// 根据解析结果构造谓词节点 PredicateNode ，其子节点是刚刚解析出来的表达式 node，保留源码位置信息。

// 示例1：单表达式谓词
//
//	if x > 0 { ... }
//
// 流程：
//
//   - 设置 withBrackets = false。
//   - 调用 parseExpression(0) 解析 x > 0。
//   - 返回 PredicateNode{Node: BinaryNode(">", x, 0)}。
//
// 示例2：多语句谓词
//
//	where { age > 18; name != "" }
//
// 流程：
//
//   - 遇到 { ，设置 withBrackets = true 。
//   - 调用 parseSequenceExpression() 解析 age > 18; name != "" 。
//   - 检查闭合 } 。
//   - 返回 PredicateNode{Node: SequenceNode[...]}。
//
// 示例3：错误情况
//
//	if x > 0; { ... }
//
// 流程：
//
//   - 触发错误 "wrap predicate with brackets { and }"。
func (p *parser) parsePredicate() Node {
	startToken := p.current
	withBrackets := false
	if p.current.Is(Bracket, "{") {
		p.next()
		withBrackets = true
	}

	p.depth++
	var node Node
	if withBrackets {
		node = p.parseSequenceExpression()
	} else {
		node = p.parseExpression(0)
		if p.current.Is(Operator, ";") {
			p.error("wrap predicate with brackets { and }")
		}
	}
	p.depth--

	if withBrackets {
		p.expect(Bracket, "}")
	}
	predicateNode := p.createNode(&PredicateNode{
		Node: node,
	}, startToken.Location)
	if predicateNode == nil {
		return nil
	}
	return predicateNode
}

// 解析数组表达式，将类似 [1, "a", x + 2] 的代码转换为抽象语法树中的 ArrayNode 。
//
// 初始化一个空的节点列表，将要解析的每个表达式（如 1, 2+3, x>5）都作为子节点存在这个列表中。
// 匹配开头的左中括号 [ ，随后一直解析，直到遇到右中括号 ] 或发生错误。
// 如果不是第一个元素，需要有 , 分隔；如果 , 后紧跟 ]（即 [1,2,] 这种尾逗号），则直接结束（进入 end: 标签）。
// 使用 parseExpression(0) 解析当前的数组项。
// 结束时必须匹配一个 ]，否则记录错误。
// 创建 ArrayNode 节点，其包含所有子节点，位置取自开头的 token，用于报错时指出“这个数组是从哪里开始的”。
//
// 示例：
//
//	[1, 2+3, x > 5]
//
//	&ArrayNode{
//	 Nodes: []Node{
//	   IntNode(1),
//	   BinaryNode(2, '+', 3),
//	   BinaryNode(x, '>', 5),
//	 }
//	}
//
// 边界情况处理：
//
//	场景				行为
//	空数组 []		不进入循环，直接返回空的 ArrayNode。
//	末尾逗号 [1,]	goto end 跳过逗号后的元素解析。
//	缺失逗号 [1 2]	expect(Operator, ",") 抛出语法错误。
//	嵌套数组 [[1]]	parseExpression(0) 递归解析内部数组。

func (p *parser) parseArrayExpression(token Token) Node {
	nodes := make([]Node, 0)

	p.expect(Bracket, "[")
	for !p.current.Is(Bracket, "]") && p.err == nil {
		if len(nodes) > 0 {
			p.expect(Operator, ",")
			if p.current.Is(Bracket, "]") {
				goto end
			}
		}
		node := p.parseExpression(0)
		nodes = append(nodes, node)
	}
end:
	p.expect(Bracket, "]")

	node := p.createNode(&ArrayNode{Nodes: nodes}, token.Location)
	if node == nil {
		return nil
	}
	return node
}

// parseMapExpression 解析 Map 表达式，将 { "a": 1, b: 2, (x + 1): 3 } 转换为 AST 中的 MapNode 。
//
// 步骤:
// 初始化节点列表 nodes 用来存放 kv 对。
// 匹配开头的左大括号 { ，随后一直解析，直到遇到右大括号 } 或发生错误。
// 如果不是第一个 kv 对，需要有 , 分隔；如果 , 后紧跟 }（即 {1:2,} 这种尾逗号），则直接结束（进入 end: 标签）；遇到连续的 ,, 如 {a:1,,b:2} 则报错。
// 循环解析每个 kv 对：
//   - 解析 key，可以是数字、字符串、标识符，或一个完整表达式
//   - 解析冒号
//   - 解析 value 表达式
//
// 构造 PairNode 并加入 nodes 列表。
// 结束时必须匹配一个 }，否则报错。
// 构造 MapNode 并返回。
//
// 示例
//
//	{a: 1, "b": 2+3, (1+2): x}
//
//	&MapNode{
//	 Pairs: []Node{
//	   &PairNode{Key: StringNode("a"), Value: IntNode(1)},
//	   &PairNode{Key: StringNode("b"), Value: BinaryNode(2, '+', 3)},
//	   &PairNode{Key: BinaryNode(1, '+', 2), Value: IdentifierNode("x")},
//	 },
//	}
//
// 边界情况
//
//	场景					行为
//	空 Map {}			不进入循环，直接返回空的 MapNode。
//	末尾逗号 {a:1,}		goto end 跳过逗号。
//	非法 Key				报错。
//	缺失冒号 {a 1}		expect(Operator, ":") 抛出语法错误。
//	嵌套 Map				递归解析内部 Map（如 {a: {b: 2}}）。
func (p *parser) parseMapExpression(token Token) Node {
	nodes := make([]Node, 0)

	p.expect(Bracket, "{")
	for !p.current.Is(Bracket, "}") && p.err == nil {

		if len(nodes) > 0 {
			p.expect(Operator, ",")
			if p.current.Is(Bracket, "}") {
				goto end
			}
			if p.current.Is(Operator, ",") {
				p.error("unexpected token %v", p.current)
			}
		}

		var key Node
		// Map key can be one of:
		//  * number
		//  * string
		//  * identifier, which is equivalent to a string
		//  * expression, which must be enclosed in parentheses -- (1 + 2)
		if p.current.Is(Number) || p.current.Is(String) || p.current.Is(Identifier) {
			key = p.createNode(&StringNode{Value: p.current.Value}, p.current.Location)
			if key == nil {
				return nil
			}
			p.next()
		} else if p.current.Is(Bracket, "(") {
			key = p.parseExpression(0)
		} else {
			p.error("a map key must be a quoted string, a number, a identifier, or an expression enclosed in parentheses (unexpected token %v)", p.current)
		}

		p.expect(Operator, ":")

		node := p.parseExpression(0)
		pair := p.createNode(&PairNode{Key: key, Value: node}, token.Location)
		if pair == nil {
			return nil
		}
		nodes = append(nodes, pair)
	}

end:
	p.expect(Bracket, "}")

	node := p.createNode(&MapNode{Pairs: nodes}, token.Location)
	if node == nil {
		return nil
	}
	return node
}

func (p *parser) parsePostfixExpressionOrigin(node Node) Node {
	postfixToken := p.current
	for (postfixToken.Is(Operator) || postfixToken.Is(Bracket)) && p.err == nil {
		optional := postfixToken.Value == "?."

	parseToken:
		if postfixToken.Value == "." || postfixToken.Value == "?." {
			p.next()

			propertyToken := p.current
			if optional && propertyToken.Is(Bracket, "[") {
				postfixToken = propertyToken
				goto parseToken
			}
			p.next()

			if propertyToken.Kind != Identifier &&
				// Operators like "not" and "matches" are valid methods or property names.
				(propertyToken.Kind != Operator || !utils.IsValidIdentifier(propertyToken.Value)) {
				p.error("expected name")
			}

			property := p.createNode(&StringNode{Value: propertyToken.Value}, propertyToken.Location)
			if property == nil {
				return nil
			}

			chainNode, isChain := node.(*ChainNode)
			optional := postfixToken.Value == "?."

			if isChain {
				node = chainNode.Node
			}

			memberNode := p.createMemberNode(&MemberNode{
				Node:     node,
				Property: property,
				Optional: optional,
			}, propertyToken.Location)
			if memberNode == nil {
				return nil
			}

			if p.current.Is(Bracket, "(") {
				memberNode.Method = true
				node = p.createNode(&CallNode{
					Callee:    memberNode,
					Arguments: p.parseArguments([]Node{}),
				}, propertyToken.Location)
				if node == nil {
					return nil
				}
			} else {
				node = memberNode
			}

			if isChain || optional {
				node = p.createNode(&ChainNode{Node: node}, propertyToken.Location)
				if node == nil {
					return nil
				}
			}

		} else if postfixToken.Value == "[" {
			p.next()
			var from, to Node

			if p.current.Is(Operator, ":") { // slice without from [:1]
				p.next()

				if !p.current.Is(Bracket, "]") { // slice without from and to [:]
					to = p.parseExpression(0)
				}

				node = p.createNode(&SliceNode{
					Node: node,
					To:   to,
				}, postfixToken.Location)
				if node == nil {
					return nil
				}
				p.expect(Bracket, "]")

			} else {

				from = p.parseExpression(0)

				if p.current.Is(Operator, ":") {
					p.next()

					if !p.current.Is(Bracket, "]") { // slice without to [1:]
						to = p.parseExpression(0)
					}

					node = p.createNode(&SliceNode{
						Node: node,
						From: from,
						To:   to,
					}, postfixToken.Location)
					if node == nil {
						return nil
					}
					p.expect(Bracket, "]")

				} else {
					// Slice operator [:] was not found,
					// it should be just an index node.
					node = p.createNode(&MemberNode{
						Node:     node,
						Property: from,
						Optional: optional,
					}, postfixToken.Location)
					if node == nil {
						return nil
					}
					if optional {
						node = p.createNode(&ChainNode{Node: node}, postfixToken.Location)
						if node == nil {
							return nil
						}
					}
					p.expect(Bracket, "]")
				}
			}
		} else {
			break
		}
		postfixToken = p.current
	}
	return node
}

// parsePostfixExpression 解析后缀表达式（Postfix Expression），处理如属性访问、方法调用、数组切片等操作。
//
//	foo.bar            // 属性访问
//	foo?.bar           // 可选链访问
//	foo.bar()          // 方法调用
//	foo["bar"]         // 索引访问
//	foo?.["bar"]       // 可选链 + 索引
//	foo[1:3]           // 切片访问
//	foo[:3], foo[1:], foo[:]  // 各种形式的切片
//
// [可选链]
// 所有包含 ?. 的操作都会最终生成一个 ChainNode ，不管 ?. 出现在访问链的哪个位置，只要出现一次 ?. 整条链都会被包装在一个 ChainNode 中，以支持「短路」语义。
//
// 例子：
//
//	obj?.a
//	ChainNode{
//		Node: MemberNode{
//		   Node:     obj,
//		   Property: "a",
//		   Optional: true,  // 表示 `?.` 语法
//		},
//	}
//
//	obj?.a?.b
//	ChainNode{
//	   Node: MemberNode{
//	       Node: MemberNode{
//	           Node:     obj,
//	           Property: "a",
//	           Optional: true, // `?.`
//	       },
//	       Property: "b",
//	       Optional: true,  // `?.`
//	   },
//	}
//
//	obj.a?.b
//	ChainNode{
//	   Node: MemberNode{
//	       Node: MemberNode{
//	           Node:     obj,
//	           Property: "a",
//	           Optional: false,  // 普通 `.` 访问
//	       },
//	       Property: "b",
//	       Optional: true,       // `?.`
//	   },
//	}
//
//	obj.a.b?.c
//	ChainNode {
//		Node: MemberNode { // .c
//	   		Node: MemberNode { // obj.a.b
//	     		Node: MemberNode {  // obj.a
//	       			Node: IdentifierNode("obj"),
//	       			Property: "a",
//	       			Optional: false
//	     		},
//	     		Property: "b",
//	     		Optional: false
//	   		},
//	   		Property: "c",
//	   		Optional: true // `?.`
//	 	}
//	}
//
// Q: 为什么 ?. 之前的也被包进 ChainNode 来了，它不是只短路后续的访问吗？
// A:
//
//	ChainNode 会包裹整个链，但只有紧跟 ?. 的 MemberNode 会将 Optional 标记为 true。
//	即使 ?. 出现在链的中间位置（如 a.b?.c.d），整个链都需要知道前面的操作是可选的。
//	ChainNode 包裹整个链能确保可选性传递到后续所有操作。
//
// Q: 对于 user.address.city 这种，也会生成 chain 吗？
// A:
//
//	对于 user.address.city 这种连续的普通属性访问（没有 ?. 操作符），不会生成 ChainNode。
//	它的 AST 结构是简单的嵌套 MemberNode，没有任何短路逻辑。
//
//	MemberNode {
//	   Node: MemberNode {
//	       Node:     Identifier("user"),   // 根对象
//	       Property: "address",            // 第一层属性
//	       Optional: false,                // 普通访问（非可选链）
//	   },
//	   Property: "city",                   // 第二层属性
//	   Optional: false,                    // 普通访问（非可选链）
//	}
//
// 普通链式访问的执行逻辑是直接的：每个属性访问步骤都假设前一个值存在，如果不存在则抛出错误。这种情况下，不需要额外的节点来处理短路逻辑，因此 AST 结构更简单。
// 只有当表达式中包含 ?. 时，才需要 ChainNode 来实现短路语义，即在遇到 nil 时提前返回 nil 而不是继续执行。
//
// [可选链解包]
// 当左侧已经是 ChainNode 时，需要：
//   - 解包：取出内部节点以继续构建链
//   - 构建新节点：使用解包后的节点作为基础
//   - 重新封装：如果原始链存在可选性，确保整个链被 ChainNode 包裹
//
// 举例：
// 示例 1：obj?.a.b
// 解析
//  1. obj?.a → ChainNode(MemberNode(obj, a, true))
//  2. 遇到 .b，解包 ChainNode，取出 MemberNode(obj, a, true)
//  3. 创建新 MemberNode(MemberNode(obj, a, true), b, false)
//  4. 重新封装为 ChainNode(MemberNode(MemberNode(obj, a, true), b, false))
//
// 示例 2：obj.a?.b
// 解析
//  1. obj.a → MemberNode(obj, a, false)
//  2. 遇到 ?.b，创建 MemberNode(MemberNode(obj, a, false), b, true)
//  3. 封装为 ChainNode(MemberNode(MemberNode(obj, a), b))
//
// [方法调用]
//
// 示例1：普通字段访问 obj.property
//
//	MemberNode {
//		Node:     IdentifierNode("user"),
//		Property: StringNode("name"),
//		Optional: false,
//	}
//
// 示例2：方法调用 obj.method()
//
//	CallNode {
//	   Callee: MemberNode {
//	       Node:     IdentifierNode("obj"),
//	       Property: StringNode("method"),
//	       Method:   true,
//	       Optional: false
//	   },
//	   Arguments: []  // 无参数
//	}
//
// 示例3：可选调用 obj?.method()
//
//	ChainNode {
//	   Node: CallNode {
//	       Callee: MemberNode {
//	           Node:     IdentifierNode("obj"),
//	           Property: StringNode("method"),
//	           Method:   true,
//	           Optional: true
//	       },
//	       Arguments: []
//	   }
//	}
//
// 示例4：带参数的方法调用 obj.method(arg1, arg2)
//
//	CallNode {
//	   Callee: MemberNode {
//	       Node:     Identifier("obj"),
//	       Property: "method",
//	       Method:   true,
//	   },
//	   Arguments: [
//	       Identifier("arg1"),
//	       Identifier("arg2"),
//	   ],
//	}
func (p *parser) parsePostfixExpression(node Node) Node {
	// 循环检查当前 token 是否是操作符或括号（如 .、?.、[），如果是，就继续解析，直到遇到非后缀操作符或者出错为止。
	postfixToken := p.current
	for (postfixToken.Is(Operator) || postfixToken.Is(Bracket)) && p.err == nil {
		optional := postfixToken.Value == "?."

	parseToken:
		// 处理形如 obj.prop 或 obj?.prop 的表达式
		if postfixToken.Value == "." || postfixToken.Value == "?." {
			p.next() // 跳过当前 . 或 ?. ，读取下一个 token

			// 保存当前 token
			propertyToken := p.current
			// 如果当前字符是 [ ，意味着解析到 ?.[ ，需按照索引访问方式来解析
			if optional && propertyToken.Is(Bracket, "[") { // 形如 foo?.["bar"]
				postfixToken = propertyToken
				goto parseToken
			}

			// 跳过当前 token
			p.next()

			// 检查 propertyToken 是否是一个合法的属性名或方法名，确保跟在点操作符（. 或 ?.）后面的名称是有效的。
			//
			// 只有两类 token 可以作为属性名或方法名：
			//	- 普通标识符（变量名、字段名）
			//	- 部分操作符（如 not、matches），满足 IsValidIdentifier
			//
			// 示例：
			//	- obj.name     // "name" 是 Identifier
			//  - obj.not      // "not" 是 Operator，但允许作为方法名
			//	- obj.matches  // "matches" 是 Operator
			//  - obj.+        // "+" 是 Operator ，但不允许作为属性名 → 报错
			//  - obj.123      // 数字，不是合法标识符 → 报错
			//  - obj.@name    // 非法标识符 → 报错
			if propertyToken.Kind != Identifier &&
				// Operators like "not" and "matches" are valid methods or property names.
				(propertyToken.Kind != Operator || !utils.IsValidIdentifier(propertyToken.Value)) {
				p.error("expected name")
			}

			// 将属性名或方法名包装成 StringNode，用于后续构建 MemberNode。
			property := p.createNode(&StringNode{Value: propertyToken.Value}, propertyToken.Location)
			if property == nil {
				return nil
			}

			// 如果左侧是 ChainNode ，则解包拿到内部节点，用于组装 MemberNode 链
			chainNode, isChain := node.(*ChainNode)
			optional := postfixToken.Value == "?."
			if isChain {
				node = chainNode.Node
			}

			// 创建 MemberNode 封装新字段访问
			memberNode := p.createMemberNode(&MemberNode{
				Node:     node,
				Property: property,
				Optional: optional,
			}, propertyToken.Location)
			if memberNode == nil {
				return nil
			}

			// 判断是否为方法调用
			if p.current.Is(Bracket, "(") {
				memberNode.Method = true
				node = p.createNode(&CallNode{
					Callee:    memberNode,
					Arguments: p.parseArguments([]Node{}),
				}, propertyToken.Location)
				if node == nil {
					return nil
				}
			} else {
				node = memberNode
			}

			// 如果之前已经是可选链、或者当前是 ?. 可选操作符，就封装为 Chain
			if isChain || optional {
				node = p.createNode(&ChainNode{Node: node}, propertyToken.Location)
				if node == nil {
					return nil
				}
			}

		} else if postfixToken.Value == "[" {
			p.next()          // 跳过 '['
			var from, to Node // 存储切片范围

			// 情况1：[:3] 或 [:]
			if p.current.Is(Operator, ":") { // slice without from [:1]
				p.next()                         // 跳过冒号 :
				if !p.current.Is(Bracket, "]") { // 如果不是右括号，则解析 to
					to = p.parseExpression(0)
				}
				node = p.createNode(&SliceNode{ // 创建切片节点
					Node: node,
					To:   to,
				}, postfixToken.Location)
				if node == nil {
					return nil
				}
				p.expect(Bracket, "]") // 期望右括号
			} else {
				// 情况2：[1:3] 或 [1:]

				from = p.parseExpression(0) // 解析 from
				if p.current.Is(Operator, ":") {
					p.next()                         // 跳过冒号 :
					if !p.current.Is(Bracket, "]") { // 如果不是右括号，则解析 to
						to = p.parseExpression(0)
					}
					node = p.createNode(&SliceNode{ // 创建切片节点
						Node: node,
						From: from,
						To:   to,
					}, postfixToken.Location)
					if node == nil {
						return nil
					}
					p.expect(Bracket, "]") // 期望右括号

				} else {
					// 情况3：普通索引 [3]
					node = p.createNode(&MemberNode{
						Node:     node,
						Property: from, // from 实际上是索引值
						Optional: optional,
					}, postfixToken.Location)
					if node == nil {
						return nil
					}
					// 可选链（?.）适用于普通索引（如 arr?.[3]）
					if optional {
						node = p.createNode(&ChainNode{Node: node}, postfixToken.Location)
						if node == nil {
							return nil
						}
					}
					p.expect(Bracket, "]") // 期望右括号
				}
			}
		} else {
			// 如果当前 token 不是成员访问 `.` 或 `?.` ，或者数组访问 `[` ，则跳出循环
			break
		}
		postfixToken = p.current
	}
	return node
}

// 解析类似 a > b、x == y 或链式比较 a < b < c 这样的表达式。
//
// 对于链式比较：
//
//	a < b < c
//	x == y != z
//	a <= b > c <= d
//
// 会被解析为逻辑与操作：
//
//	(a < b) && (b < c)
//	(x == y) && (y != z)
//	(a <= b) && (b > c) && (c <= d)
//
// 示例 1：简单比较 a > b
//
// 初始状态：
//   - left = a（已解析的左侧）
//   - token = >（当前操作符）
//
// 执行流程：
//   - 解析 b → comparator = b
//   - 创建 BinaryNode(Operator: ">", Left: a, Right: b)
//   - rootNode = BinaryNode(a > b)
//   - 下一个 token 不是比较操作符 → 退出循环
//
// 最终 AST：
//
//	BinaryNode {
//	   Operator: ">",
//	   Left:     Identifier("a"),
//	   Right:    Identifier("b"),
//	}
//
// 示例 2：链式比较 a < b < c
//
// 初始状态：
//   - left = a（已解析的左侧）
//   - token = <（当前操作符）
//
// 第一次循环：
//   - 解析 b → comparator = b
//   - 创建 BinaryNode(Operator: "<", Left: a, Right: b)
//   - rootNode = BinaryNode(a < b)
//   - left = b（下一次比较的左表达式）
//
// 第二次循环：
//   - 解析 c → comparator = c
//   - 创建 BinaryNode(Operator: "<", Left: b, Right: c)
//   - 用 && 连接之前的 rootNode 和新的比较：
//     BinaryNode {
//     Operator: "&&",
//     Left:     BinaryNode(a < b),
//     Right:    BinaryNode(b < c),
//     }
//
// 最终 AST：
//
//	BinaryNode {
//	   Operator: "&&",
//	   Left: BinaryNode {
//	       Operator: "<",
//	       Left:     Identifier("a"),
//	       Right:    Identifier("b"),
//	   },
//	   Right: BinaryNode {
//	       Operator: "<",
//	       Left:     Identifier("b"),
//	       Right:    Identifier("c"),
//	   },
//	}
//
// 语义等价于：(a < b) && (b < c)。
//
// 示例 3：混合比较 x == y != z
// 第一次循环：
//   - 解析 y → comparator = y
//   - 创建 BinaryNode(Operator: "==", Left: x, Right: y)
//   - rootNode = BinaryNode(x == y)
//   - left = y
//
// 第二次循环：
//   - 解析 z → comparator = z
//   - 创建 BinaryNode(Operator: "!=", Left: y, Right: z)
//   - 用 && 连接：
//     BinaryNode {
//     Operator: "&&",
//     Left:     BinaryNode(x == y),
//     Right:    BinaryNode(y != z),
//     }
//
// 最终 AST：
//
//	BinaryNode {
//	   Operator: "&&",
//	   Left: BinaryNode {
//	       Operator: "==",
//	       Left:     Identifier("x"),
//	       Right:    Identifier("y"),
//	   },
//	   Right: BinaryNode {
//	       Operator: "!=",
//	       Left:     Identifier("y"),
//	       Right:    Identifier("z"),
//	   },
//	}
//
// 语义等价于：(x == y) && (y != z)。
//
// 关键设计点
//   - 链式比较重构：通过 && 连接多个比较，确保语义正确（如 a < b < c 等价于 a < b 和 b < c）。
//   - 优先级控制： parseExpression(precedence + 1) 确保右侧表达式优先计算（如 a > b + c 会先解析 b + c）。
//   - 左递归转右递归：通过循环和 left = comparator 实现链式比较的解析，每次循环将当前右侧作为下一次左侧，实现 a < b < c 的从左到右解析顺序，避免递归爆栈。
//
// 边界情况
//   - 单个比较（如 a > b）：直接返回 BinaryNode。
//   - 空表达式：如果 parseExpression 返回 nil，整个函数返回 nil。
//   - 非法操作符：如果下一个 token 不是比较操作符，循环终止。
func (p *parser) parseComparison(left Node, token Token, precedence int) Node {
	var rootNode Node
	for {
		// 解析右侧表达式（优先级高于当前比较操作）
		comparator := p.parseExpression(precedence + 1)
		// 创建当前比较节点（如 a < b）
		cmpNode := p.createNode(&BinaryNode{
			Operator: token.Value, // 比较操作符（如 <, >, ==）
			Left:     left,        // 左侧表达式
			Right:    comparator,  // 右侧表达式
		}, token.Location)
		if cmpNode == nil {
			return nil
		}

		// 构建逻辑与链
		if rootNode == nil {
			rootNode = cmpNode // 第一个比较表达式
		} else {
			rootNode = p.createNode(&BinaryNode{ // 将新比较表达式与之前的结果用 && 连接
				Operator: "&&",
				Left:     rootNode,
				Right:    cmpNode,
			}, token.Location)
			if rootNode == nil {
				return nil
			}
		}

		// 更新左侧表达式为当前比较的右侧，继续循环
		left = comparator
		token = p.current
		if !(token.Is(Operator) && operator.IsComparison(token.Value) && p.err == nil) { // 检查是否还有更多比较操作符
			break
		}
		p.next()
	}
	return rootNode
}
