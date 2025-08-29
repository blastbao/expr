package vm

import (
	"context"
	"fmt"
)

var CustomFuncTypes = []any{
	1: new(func(context.Context, int) (int, error)),
	2: new(func(context.Context, int) (int, error)),
	//3: new(func(*gcontext.GraphEngineCtx, int) (int, error)),
}

func (vm *VM) callCustomFuncType(fn any, kind int) (any, error) {
	switch kind {
	//case 3:
	//	arg2 := vm.pop().(int)
	//	arg1 := vm.pop()
	//	var gctx *gcontext.GraphEngineCtx
	//	if arg1 != nil {
	//		gctx = arg1.(*gcontext.GraphEngineCtx)
	//	}
	//	return fn.(func(*gcontext.GraphEngineCtx, int) (int, error))(gctx, arg2)
	}
	panic(fmt.Sprintf("unknown function kind (%v)", kind))
}
