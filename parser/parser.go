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

func (p *parser) parseMapExpression(token Token) Node {
	p.expect(Bracket, "{")

	nodes := make([]Node, 0)
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

func (p *parser) parsePostfixExpression(node Node) Node {
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

func (p *parser) parseComparison(left Node, token Token, precedence int) Node {
	var rootNode Node
	for {
		comparator := p.parseExpression(precedence + 1)
		cmpNode := p.createNode(&BinaryNode{
			Operator: token.Value,
			Left:     left,
			Right:    comparator,
		}, token.Location)
		if cmpNode == nil {
			return nil
		}
		if rootNode == nil {
			rootNode = cmpNode
		} else {
			rootNode = p.createNode(&BinaryNode{
				Operator: "&&",
				Left:     rootNode,
				Right:    cmpNode,
			}, token.Location)
			if rootNode == nil {
				return nil
			}
		}

		left = comparator
		token = p.current
		if !(token.Is(Operator) && operator.IsComparison(token.Value) && p.err == nil) {
			break
		}
		p.next()
	}
	return rootNode
}
