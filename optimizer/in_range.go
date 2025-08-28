package optimizer

import (
	"reflect"

	. "github.com/expr-lang/expr/ast"
)

// inRange 把 x in (m .. n) 改写成：(x >= m) and (x <= n)
//
// 优化示例
//
//	优化前：age in 18..65
//	优化后：age >= 18 and age <= 65
//
// 对应的 AST 结构变化：
//
//	优化前：
//	   in
//	  / \
//	age  ..
//		/ \
//	   18  65
//
//	优化后：
//		 and
//		/   \
//	  >=     <=
//	 / \    / \
//	age 18 age 65

type inRange struct{}

func (*inRange) Visit(node *Node) {
	switch n := (*node).(type) {
	case *BinaryNode:
		if n.Operator == "in" {
			t := n.Left.Type()
			if t == nil {
				return
			}
			if t.Kind() != reflect.Int {
				return
			}
			if rangeOp, ok := n.Right.(*BinaryNode); ok && rangeOp.Operator == ".." {
				if from, ok := rangeOp.Left.(*IntegerNode); ok {
					if to, ok := rangeOp.Right.(*IntegerNode); ok {
						patchCopyType(node, &BinaryNode{
							Operator: "and",
							Left: &BinaryNode{
								Operator: ">=",
								Left:     n.Left,
								Right:    from,
							},
							Right: &BinaryNode{
								Operator: "<=",
								Left:     n.Left,
								Right:    to,
							},
						})
					}
				}
			}
		}
	}
}
