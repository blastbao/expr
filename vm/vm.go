package vm

//go:generate sh -c "go run ./func_types > ./func_types[generated].go"

import (
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/expr-lang/expr/builtin"
	"github.com/expr-lang/expr/conf"
	"github.com/expr-lang/expr/file"
	"github.com/expr-lang/expr/internal/deref"
	"github.com/expr-lang/expr/vm/runtime"
)

func Run(program *Program, env any) (any, error) {
	if program == nil {
		return nil, fmt.Errorf("program is nil")
	}
	vm := VM{}
	return vm.Run(program, env)
}

func Debug() *VM {
	vm := &VM{
		debug: true,
		step:  make(chan struct{}, 0),
		curr:  make(chan int, 0),
	}
	return vm
}

type VM struct {
	Stack        []any
	Scopes       []*Scope
	Variables    []any
	MemoryBudget uint
	ip           int
	memory       uint
	debug        bool
	step         chan struct{}
	curr         chan int
}

//type VM struct {
//	Stack        []any         // 操作数栈
//	Scopes       []*Scope      // 支持循环、排序等作用域的结构栈
//	Variables    []any         // 局部变量表
//	MemoryBudget uint          // 内存预算限制（限制栈使用）
//	ip           int           // 指令指针（当前执行到哪个字节码）
//	memory       uint          // 当前使用内存估计
//	debug        bool          // 是否开启调试
//	step         chan struct{} // 手动步进通道（debug 模式）
//	curr         chan int      // 当前执行位置输出通道（debug 模式）
//}

func (vm *VM) Run(program *Program, env any) (_ any, err error) {
	defer func() {
		if r := recover(); r != nil {
			var location file.Location
			if vm.ip-1 < len(program.locations) {
				location = program.locations[vm.ip-1]
			}
			f := &file.Error{
				Location: location,
				Message:  fmt.Sprintf("%v", r),
			}
			if err, ok := r.(error); ok {
				f.Wrap(err)
			}
			err = f.Bind(program.source)
		}
	}()

	if vm.Stack == nil {
		vm.Stack = make([]any, 0, 2)
	} else {
		vm.Stack = vm.Stack[0:0]
	}
	if vm.Scopes != nil {
		vm.Scopes = vm.Scopes[0:0]
	}
	if len(vm.Variables) < program.variables {
		vm.Variables = make([]any, program.variables)
	}
	if vm.MemoryBudget == 0 {
		vm.MemoryBudget = conf.DefaultMemoryBudget
	}
	vm.memory = 0
	vm.ip = 0

	for vm.ip < len(program.Bytecode) {
		if debug && vm.debug {
			<-vm.step
		}

		op := program.Bytecode[vm.ip]
		arg := program.Arguments[vm.ip]
		vm.ip += 1

		switch op {
		case OpInvalid:
			panic("invalid opcode")
		case OpPush:
			// 将第 arg 个常量入栈
			vm.push(program.Constants[arg])
		case OpInt:
			// 将字面量 arg 入栈
			vm.push(arg)
		case OpPop:
			// 出栈
			vm.pop()
		case OpStore:
			// 把栈顶元素弹出并赋值给变量 vars[arg]
			vm.Variables[arg] = vm.pop()
		case OpLoadVar:
			// 把变量 vars[arg] 入栈
			vm.push(vm.Variables[arg])
		case OpLoadConst:
			// 从 env 中获取第 arg 个常量的值
			vm.push(runtime.Fetch(env, program.Constants[arg]))
		case OpLoadField:
			// 从 env 中获取第 arg 个常量所表示的嵌套字段的值
			vm.push(runtime.FetchField(env, program.Constants[arg].(*runtime.Field)))
		case OpLoadFast:
			// 从 env 中获取第 arg 个常量的值，这里常量是字符串类型
			vm.push(env.(map[string]any)[program.Constants[arg].(string)])
		case OpLoadMethod:
			// 从 env 中获取第 arg 个常量所表示的方法下标
			vm.push(runtime.FetchMethod(env, program.Constants[arg].(*runtime.Method)))
		case OpLoadFunc:
			// 把第 arg 个函数入栈
			vm.push(program.functions[arg])
		case OpFetch:
			// 从 a 中获取 b 值 c ，然后入栈
			b := vm.pop()
			a := vm.pop()
			vm.push(runtime.Fetch(a, b))
		case OpFetchField:
			a := vm.pop()
			vm.push(runtime.FetchField(a, program.Constants[arg].(*runtime.Field)))
		case OpLoadEnv:
			vm.push(env)
		case OpMethod:
			a := vm.pop()
			vm.push(runtime.FetchMethod(a, program.Constants[arg].(*runtime.Method)))
		case OpTrue:
			vm.push(true)
		case OpFalse:
			vm.push(false)
		case OpNil:
			vm.push(nil)
		case OpNegate:
			v := runtime.Negate(vm.pop())
			vm.push(v)
		case OpNot:
			v := vm.pop().(bool)
			vm.push(!v)
		case OpEqual:
			b := vm.pop()
			a := vm.pop()
			vm.push(runtime.Equal(a, b))
		case OpEqualInt:
			b := vm.pop()
			a := vm.pop()
			vm.push(a.(int) == b.(int))
		case OpEqualString:
			b := vm.pop()
			a := vm.pop()
			vm.push(a.(string) == b.(string))
		case OpJump: // Jmp XXX ，修改 ip 跳转到指定 op ，这里都是相对寻址，基于当前 ip 作偏移
			vm.ip += arg
		case OpJumpIfTrue:
			if vm.current().(bool) {
				vm.ip += arg
			}
		case OpJumpIfFalse:
			if !vm.current().(bool) {
				vm.ip += arg
			}
		case OpJumpIfNil:
			if runtime.IsNil(vm.current()) {
				vm.ip += arg
			}
		case OpJumpIfNotNil:
			if !runtime.IsNil(vm.current()) {
				vm.ip += arg
			}
		case OpJumpIfEnd:
			scope := vm.scope()
			if scope.Index >= scope.Len {
				vm.ip += arg
			}
		case OpJumpBackward:
			vm.ip -= arg
		case OpIn:
			b := vm.pop()
			a := vm.pop()
			vm.push(runtime.In(a, b))
		case OpLess:
			b := vm.pop()
			a := vm.pop()
			vm.push(runtime.Less(a, b))
		case OpMore:
			b := vm.pop()
			a := vm.pop()
			vm.push(runtime.More(a, b))
		case OpLessOrEqual:
			b := vm.pop()
			a := vm.pop()
			vm.push(runtime.LessOrEqual(a, b))
		case OpMoreOrEqual:
			b := vm.pop()
			a := vm.pop()
			vm.push(runtime.MoreOrEqual(a, b))
		case OpAdd:
			b := vm.pop()
			a := vm.pop()
			vm.push(runtime.Add(a, b))
		case OpSubtract:
			b := vm.pop()
			a := vm.pop()
			vm.push(runtime.Subtract(a, b))
		case OpMultiply:
			b := vm.pop()
			a := vm.pop()
			vm.push(runtime.Multiply(a, b))
		case OpDivide:
			b := vm.pop()
			a := vm.pop()
			vm.push(runtime.Divide(a, b))
		case OpModulo:
			b := vm.pop()
			a := vm.pop()
			vm.push(runtime.Modulo(a, b))
		case OpExponent:
			b := vm.pop()
			a := vm.pop()
			vm.push(runtime.Exponent(a, b))
		case OpRange:
			b := vm.pop()
			a := vm.pop()
			min := runtime.ToInt(a)
			max := runtime.ToInt(b)
			size := max - min + 1
			if size <= 0 {
				size = 0
			}
			vm.memGrow(uint(size))
			vm.push(runtime.MakeRange(min, max))
		case OpMatches:
			b := vm.pop()
			a := vm.pop()
			if runtime.IsNil(a) || runtime.IsNil(b) {
				vm.push(false)
				break
			}
			match, err := regexp.MatchString(b.(string), a.(string))
			if err != nil {
				panic(err)
			}
			vm.push(match)
		case OpMatchesConst:
			a := vm.pop()
			if runtime.IsNil(a) {
				vm.push(false)
				break
			}
			r := program.Constants[arg].(*regexp.Regexp)
			vm.push(r.MatchString(a.(string)))
		case OpContains:
			b := vm.pop()
			a := vm.pop()
			if runtime.IsNil(a) || runtime.IsNil(b) {
				vm.push(false)
				break
			}
			vm.push(strings.Contains(a.(string), b.(string)))
		case OpStartsWith:
			b := vm.pop()
			a := vm.pop()
			if runtime.IsNil(a) || runtime.IsNil(b) {
				vm.push(false)
				break
			}
			vm.push(strings.HasPrefix(a.(string), b.(string)))
		case OpEndsWith:
			b := vm.pop()
			a := vm.pop()
			if runtime.IsNil(a) || runtime.IsNil(b) {
				vm.push(false)
				break
			}
			vm.push(strings.HasSuffix(a.(string), b.(string)))
		case OpSlice:
			from := vm.pop()
			to := vm.pop()
			node := vm.pop()
			vm.push(runtime.Slice(node, from, to))
		case OpCall:
			// 获取待调用的函数，反射得到类型
			fn := reflect.ValueOf(vm.pop())
			// 从栈中弹出指定数量（arg）的参数
			size := arg
			in := make([]reflect.Value, size)
			for i := int(size) - 1; i >= 0; i-- {
				param := vm.pop()
				if param == nil {
					in[i] = reflect.Zero(fn.Type().In(i)) // 根据参数类型生成反射零值
				} else {
					in[i] = reflect.ValueOf(param)
				}
			}
			// 通过反射调用函数
			out := fn.Call(in)
			// 如果有两个返回值，且第二个是 error 类型且非 nil → 抛出异常，等价于 ```if err != nil { panic(err) }```
			if len(out) == 2 && out[1].Type() == errorType && !out[1].IsNil() {
				panic(out[1].Interface().(error))
			}
			// 将第一个返回值（通常是实际的结果）压入虚拟机栈中，如果有多个返回值，其余返回值被丢弃。
			vm.push(out[0].Interface())
		case OpCall0:
			out, err := program.functions[arg]()
			if err != nil {
				panic(err)
			}
			vm.push(out)
		case OpCall1:
			a := vm.pop()
			out, err := program.functions[arg](a)
			if err != nil {
				panic(err)
			}
			vm.push(out)
		case OpCall2:
			b := vm.pop()
			a := vm.pop()
			out, err := program.functions[arg](a, b)
			if err != nil {
				panic(err)
			}
			vm.push(out)
		case OpCall3:
			c := vm.pop()
			b := vm.pop()
			a := vm.pop()
			out, err := program.functions[arg](a, b, c)
			if err != nil {
				panic(err)
			}
			vm.push(out)
		case OpCallN:
			fn := vm.pop().(Function)
			size := arg
			in := make([]any, size)
			for i := int(size) - 1; i >= 0; i-- {
				in[i] = vm.pop()
			}
			out, err := fn(in...)
			if err != nil {
				panic(err)
			}
			vm.push(out)
		case OpCallFast:
			fn := vm.pop().(func(...any) any)
			size := arg
			in := make([]any, size)
			for i := int(size) - 1; i >= 0; i-- {
				in[i] = vm.pop()
			}
			vm.push(fn(in...))
		case OpCallSafe:
			fn := vm.pop().(SafeFunction)
			size := arg
			in := make([]any, size)
			for i := int(size) - 1; i >= 0; i-- {
				in[i] = vm.pop()
			}
			out, mem, err := fn(in...)
			if err != nil {
				panic(err)
			}
			vm.memGrow(mem)
			vm.push(out)
		case OpCallTyped:
			vm.push(vm.call(vm.pop(), arg))
		case OpCallBuiltin1:
			vm.push(builtin.Builtins[arg].Fast(vm.pop()))
		case OpArray:
			size := vm.pop().(int)
			vm.memGrow(uint(size))
			array := make([]any, size)
			for i := size - 1; i >= 0; i-- {
				array[i] = vm.pop()
			}
			vm.push(array)
		case OpMap:
			size := vm.pop().(int)
			vm.memGrow(uint(size))
			m := make(map[string]any)
			for i := size - 1; i >= 0; i-- {
				value := vm.pop()
				key := vm.pop()
				m[key.(string)] = value
			}
			vm.push(m)
		case OpLen:
			vm.push(runtime.Len(vm.current()))
		case OpCast:
			switch arg {
			case 0:
				vm.push(runtime.ToInt(vm.pop()))
			case 1:
				vm.push(runtime.ToInt64(vm.pop()))
			case 2:
				vm.push(runtime.ToFloat64(vm.pop()))
			}
		case OpDeref:
			a := vm.pop()
			vm.push(deref.Interface(a))
		case OpIncrementIndex:
			vm.scope().Index++
		case OpDecrementIndex:
			scope := vm.scope()
			scope.Index--
		case OpIncrementCount:
			scope := vm.scope()
			scope.Count++
		case OpGetIndex:
			vm.push(vm.scope().Index)
		case OpGetCount:
			scope := vm.scope()
			vm.push(scope.Count)
		case OpGetLen:
			scope := vm.scope()
			vm.push(scope.Len)
		case OpGetAcc:
			vm.push(vm.scope().Acc)
		case OpSetAcc:
			vm.scope().Acc = vm.pop()
		case OpSetIndex:
			scope := vm.scope()
			scope.Index = vm.pop().(int)
		case OpPointer:
			scope := vm.scope()
			vm.push(scope.Array.Index(scope.Index).Interface())
		case OpThrow:
			panic(vm.pop().(error))
		case OpCreate:
			switch arg {
			case 1:
				vm.push(make(groupBy))
			case 2:
				scope := vm.scope()
				var desc bool
				switch vm.pop().(string) {
				case "asc":
					desc = false
				case "desc":
					desc = true
				default:
					panic("unknown order, use asc or desc")
				}
				vm.push(&runtime.SortBy{
					Desc:   desc,
					Array:  make([]any, 0, scope.Len),
					Values: make([]any, 0, scope.Len),
				})
			default:
				panic(fmt.Sprintf("unknown OpCreate argument %v", arg))
			}
		case OpGroupBy:
			scope := vm.scope()
			key := vm.pop()
			item := scope.Array.Index(scope.Index).Interface()
			scope.Acc.(groupBy)[key] = append(scope.Acc.(groupBy)[key], item)
		case OpSortBy:
			scope := vm.scope()
			value := vm.pop()
			item := scope.Array.Index(scope.Index).Interface()
			sortable := scope.Acc.(*runtime.SortBy)
			sortable.Array = append(sortable.Array, item)
			sortable.Values = append(sortable.Values, value)
		case OpSort:
			scope := vm.scope()
			sortable := scope.Acc.(*runtime.SortBy)
			sort.Sort(sortable)
			vm.memGrow(uint(scope.Len))
			vm.push(sortable.Array)
		case OpProfileStart:
			span := program.Constants[arg].(*Span)
			span.start = time.Now()
		case OpProfileEnd:
			span := program.Constants[arg].(*Span)
			span.Duration += time.Since(span.start).Nanoseconds()
		case OpBegin:
			a := vm.pop()
			array := reflect.ValueOf(a)
			vm.Scopes = append(vm.Scopes, &Scope{
				Array: array,
				Len:   array.Len(),
			})
		case OpEnd:
			vm.Scopes = vm.Scopes[:len(vm.Scopes)-1]
		default:
			panic(fmt.Sprintf("unknown bytecode %#x", op))
		}
		if debug && vm.debug {
			vm.curr <- vm.ip
		}
	}

	if debug && vm.debug {
		close(vm.curr)
		close(vm.step)
	}

	if len(vm.Stack) > 0 {
		return vm.pop(), nil
	}
	return nil, nil
}

func (vm *VM) current() any {
	return vm.Stack[len(vm.Stack)-1]
}

func (vm *VM) push(value any) {
	vm.Stack = append(vm.Stack, value)
}

func (vm *VM) pop() any {
	value := vm.Stack[len(vm.Stack)-1]
	vm.Stack = vm.Stack[:len(vm.Stack)-1]
	return value
}

func (vm *VM) memGrow(size uint) {
	vm.memory += size
	if vm.memory >= vm.MemoryBudget {
		panic("memory budget exceeded")
	}
}

func (vm *VM) scope() *Scope {
	return vm.Scopes[len(vm.Scopes)-1]
}

func (vm *VM) Step() {
	vm.step <- struct{}{}
}

func (vm *VM) Position() chan int {
	return vm.curr
}
