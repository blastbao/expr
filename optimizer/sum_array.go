package optimizer

import (
	"fmt"

	. "github.com/expr-lang/expr/ast"
)

// 转换过程示例
//
// 原始表达式：
//	sum([a, b, c, d])
//
// 转换过程（递归分解）：
//	1. sumArrayFold([a, b, c, d])
//	2. → a + sumArrayFold([b, c, d])
//	3. → a + (b + sumArrayFold([c, d]))
//	4. → a + (b + (c + d))
//
// 最终结果：
//	a + b + c + d
//
// 过程举例：
//	情况1：数组长度 > 2
//	return &BinaryNode{
//		Operator: "+",
//		Left:     array.Nodes[0],        // 第一个元素
//		Right:    sumArrayFold(&ArrayNode{Nodes: array.Nodes[1:]}), // 递归处理剩余元素
//	}
//	∙ 将数组分解为 第一个元素 + (剩余元素的和)
//	∙ 递归处理直到数组长度为2
//
//	情况2：数组长度 == 2
//	return &BinaryNode{
//		Operator: "+",
//		Left:     array.Nodes[0],
//		Right:    array.Nodes[1],
//	}
//	∙ 直接返回两个元素的加法

type sumArray struct{}

func (*sumArray) Visit(node *Node) {
	if sumBuiltin, ok := (*node).(*BuiltinNode); ok && sumBuiltin.Name == "sum" && len(sumBuiltin.Arguments) == 1 {
		if array, ok := sumBuiltin.Arguments[0].(*ArrayNode); ok &&
			len(array.Nodes) >= 2 {
			patchCopyType(node, sumArrayFold(array))
		}
	}
}

// sumArrayFold 函数是递归的，把数组 [a, b, c, d] 展开为嵌套的二元加法表达式：
//	- 如果数组长度 > 2：
//		- 把第一个元素取出来（array.Nodes[0]）
//		- 递归处理剩下的元素
//		- 生成 BinaryNode{ Left: a, Right: (b+c+d), Operator: "+" }
//	- 如果数组长度 = 2：
//		- 直接返回 BinaryNode{ Left: a, Right: b, Operator: "+" }
//	- 否则抛错。

func sumArrayFold(array *ArrayNode) *BinaryNode {
	if len(array.Nodes) > 2 {
		return &BinaryNode{
			Operator: "+",
			Left:     array.Nodes[0],
			Right:    sumArrayFold(&ArrayNode{Nodes: array.Nodes[1:]}),
		}
	} else if len(array.Nodes) == 2 {
		return &BinaryNode{
			Operator: "+",
			Left:     array.Nodes[0],
			Right:    array.Nodes[1],
		}
	}
	panic(fmt.Errorf("sumArrayFold: invalid array length %d", len(array.Nodes)))
}
