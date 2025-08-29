package expr

import (
	"context"
	"github.com/expr-lang/expr/vm"
)

var FuncTypes = []any{
	1001: new(func(context.Context, int) int),
	1002: new(func(context.Context, int) (int, error)),
}

func CallFuncType(v *vm.VM, fn any, kind int) {

}
