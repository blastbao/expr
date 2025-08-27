package patcher

import (
	"fmt"
	"reflect"

	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/builtin"
	"github.com/expr-lang/expr/checker/nature"
	"github.com/expr-lang/expr/conf"
)

// OperatorOverloading 在 AST 遍历过程中，将某个二元运算符节点替换成对应的函数调用节点，从而实现自定义运算符的行为。
type OperatorOverloading struct {
	Operator  string              // 需要重载的运算符，比如 "+"
	Overloads []string            // 用于替换运算符的候选函数名列表
	Env       *nature.Nature      // 环境类型信息（存储变量/类型定义）
	Functions conf.FunctionsTable // 全局函数表
	applied   bool                // 标记这次遍历是否对 AST 做过修改（用于重复执行判断）
}

func (p *OperatorOverloading) Visit(node *ast.Node) {
	// 仅处理二元运算节点（如 a + b）
	binaryNode, ok := (*node).(*ast.BinaryNode)
	if !ok {
		return
	}

	// 仅处理目标运算符（如只重载"+"）
	if binaryNode.Operator != p.Operator {
		return
	}

	// 获取左右操作数的类型
	leftType := binaryNode.Left.Type()
	rightType := binaryNode.Right.Type()

	// 查找匹配的重载函数
	ret, fn, ok := p.FindSuitableOperatorOverload(leftType, rightType)
	if ok {
		// 替换二元运算节点为函数调用节点（如 a + b → Add(a, b)）
		newNode := &ast.CallNode{
			Callee:    &ast.IdentifierNode{Value: fn},                // 函数名
			Arguments: []ast.Node{binaryNode.Left, binaryNode.Right}, // 左右操作数
		}
		newNode.SetType(ret)     // 设置返回类型
		ast.Patch(node, newNode) // 替换 AST 节点
		p.applied = true         // 标记 AST 被修改
	}
}

// Reset 重置状态（每轮遍历前调用）
// Tracking must be reset before every walk over the AST tree
func (p *OperatorOverloading) Reset() {
	p.applied = false
}

// ShouldRepeat 判断是否需要继续下一轮遍历（若本轮修改了AST，则需重复）
//
// 某些情况下，替换运算符后可能产生新的二元表达式，需要重复执行直到无修改。
func (p *OperatorOverloading) ShouldRepeat() bool {
	return p.applied
}

// FindSuitableOperatorOverload 根据左右操作数的类型，从 函数表/Env 中找到参数类型匹配的函数。
func (p *OperatorOverloading) FindSuitableOperatorOverload(l, r reflect.Type) (reflect.Type, string, bool) {
	t, fn, ok := p.findSuitableOperatorOverloadInFunctions(l, r)
	if !ok {
		t, fn, ok = p.findSuitableOperatorOverloadInTypes(l, r)
	}
	return t, fn, ok
}

// 从环境类型中查找匹配的方法（如结构体的成员方法）
func (p *OperatorOverloading) findSuitableOperatorOverloadInTypes(l, r reflect.Type) (reflect.Type, string, bool) {
	for _, fn := range p.Overloads {
		fnType, ok := p.Env.Get(fn) // 从环境获取类型/方法
		if !ok {
			continue
		}
		firstInIndex := 0
		if fnType.Method { // 若是方法，第一个参数是接收者
			firstInIndex = 1 // As first argument to method is receiver.
		}
		ret, done := checkTypeSuits(fnType.Type, l, r, firstInIndex)
		if done {
			return ret, fn, true
		}
	}
	return nil, "", false
}

// 从函数表中查找匹配的重载函数
func (p *OperatorOverloading) findSuitableOperatorOverloadInFunctions(l, r reflect.Type) (reflect.Type, string, bool) {
	for _, fn := range p.Overloads {
		fnType, ok := p.Functions[fn] // 从函数表获取函数
		if !ok {
			continue
		}
		// 检查函数的所有重载类型是否匹配
		firstInIndex := 0
		for _, overload := range fnType.Types {
			ret, done := checkTypeSuits(overload, l, r, firstInIndex)
			if done {
				return ret, fn, true
			}
		}
	}
	return nil, "", false
}

func checkTypeSuits(t reflect.Type, l reflect.Type, r reflect.Type, firstInIndex int) (reflect.Type, bool) {
	firstArgType := t.In(firstInIndex)      // 第一个参数类型
	secondArgType := t.In(firstInIndex + 1) // 第二个参数类型
	// 检查左操作数是否匹配第一个参数（支持接口实现）
	firstFit := l == firstArgType || (firstArgType.Kind() == reflect.Interface && (l == nil || l.Implements(firstArgType)))
	// 检查右操作数是否匹配第二个参数
	secondFit := r == secondArgType || (secondArgType.Kind() == reflect.Interface && (r == nil || r.Implements(secondArgType)))
	if firstFit && secondFit {
		return t.Out(0), true // 返回函数返回类型
	}
	return nil, false
}

func (p *OperatorOverloading) Check() {
	// 校验所有候选函数是否存在且签名正确
	for _, fn := range p.Overloads {
		// 检查函数是否在环境或函数表中存在
		fnType, foundType := p.Env.Get(fn)
		fnFunc, foundFunc := p.Functions[fn]
		if !foundFunc && (!foundType || fnType.Type.Kind() != reflect.Func) {
			panic(fmt.Errorf("函数 %s 不存在", fn))
		}

		// 检查 Env 中的函数是否合法
		if foundType {
			checkType(fnType, fn, p.Operator)
		}
		// 检查函数表中的函数是否合法
		if foundFunc {
			checkFunc(fnFunc, fn, p.Operator)
		}
	}
}

// checkType 要求：
//
// 输入参数数量：
//   - 普通函数：必须接收 2 个参数（对应运算符的左、右操作数，如 a + b 中的 a 和 b）。
//   - 成员方法：必须接收 3 个参数（第一个是方法接收者，后两个是操作数，如 t.Add(d) 中 t 是接收者，d 是操作数）。
//
// 返回值数量：
//   - 必须返回 1 个值
func checkType(fnType nature.Nature, fn string, operator string) {
	// 确定所需的输入参数数量
	requiredNumIn := 2 // 普通函数需要2个输入参数（对应运算符的左右操作数）
	if fnType.Method {
		// 如果是方法（如结构体的成员方法），第一个参数是接收者，因此需要3个输入参数
		requiredNumIn = 3
	}
	// 校验参数和返回值数量是否符合要求
	if fnType.Type.NumIn() != requiredNumIn || fnType.Type.NumOut() != 1 {
		panic(fmt.Errorf("function %s for %s operator does not have a correct signature", fn, operator)) // 签名错误时抛出异常
	}
}

// checkFunc 要求：
// - 函数必须有至少一种类型定义（len(fn.Types) > 0）。
// - 每种重载类型必须接收 2 个参数、返回 1 个值（与普通函数的要求一致，因函数表中的函数无接收者）。
func checkFunc(fn *builtin.Function, name string, operator string) {
	if len(fn.Types) == 0 {
		panic(fmt.Errorf("function %q for %q operator misses types", name, operator))
	}
	for _, t := range fn.Types {
		if t.NumIn() != 2 || t.NumOut() != 1 {
			panic(fmt.Errorf("function %q for %q operator does not have a correct signature", name, operator))
		}
	}
}
