package optimizer

import (
	. "github.com/expr-lang/expr/ast"
)

type sumMap struct{}

// Visit
//
// 优化前：sum(map(f, collection))
//
//	∙ 先对集合进行映射转换
//	∙ 再对结果求和
//
// 优化后： sum(f, collection)
//
//	∙ 直接对集合应用求和操作，跳过中间映射步骤
//	∙ 减少内存分配和计算开销
//
// 举例：
//
// 优化前：
//
//	sum(map(function(x) { return x * 2 }, [1, 2, 3]))
//
// 优化后：
//
//	sum(function(x) { return x * 2 }, [1, 2, 3])
func (*sumMap) Visit(node *Node) {
	if sumBuiltin, ok := (*node).(*BuiltinNode); ok &&
		sumBuiltin.Name == "sum" &&
		len(sumBuiltin.Arguments) == 1 {

		if mapBuiltin, ok := sumBuiltin.Arguments[0].(*BuiltinNode); ok &&
			mapBuiltin.Name == "map" &&
			len(mapBuiltin.Arguments) == 2 {
			patchCopyType(node, &BuiltinNode{
				Name: "sum",
				Arguments: []Node{
					mapBuiltin.Arguments[0],
					mapBuiltin.Arguments[1],
				},
			})
		}
	}
}
