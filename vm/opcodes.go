package vm

import "fmt"

type Opcode byte

const (
	OpInvalid Opcode = iota
	OpPush
	OpInt
	OpPop
	OpStore
	OpLoadVar
	OpLoadConst
	OpLoadField
	OpLoadFast
	OpLoadMethod
	OpLoadFunc
	OpLoadEnv
	OpFetch
	OpFetchField
	OpMethod
	OpTrue
	OpFalse
	OpNil
	OpNegate
	OpNot
	OpEqual
	OpEqualInt
	OpEqualString
	OpJump
	OpJumpIfTrue
	OpJumpIfFalse
	OpJumpIfNil
	OpJumpIfNotNil
	OpJumpIfEnd
	OpJumpBackward
	OpIn
	OpLess
	OpMore
	OpLessOrEqual
	OpMoreOrEqual
	OpAdd
	OpSubtract
	OpMultiply
	OpDivide
	OpModulo
	OpExponent
	OpRange
	OpMatches
	OpMatchesConst
	OpContains
	OpStartsWith
	OpEndsWith
	OpSlice
	OpCall
	OpCall0
	OpCall1
	OpCall2
	OpCall3
	OpCallN
	OpCallFast
	OpCallSafe
	OpCallTyped
	OpCallBuiltin1
	OpArray
	OpMap
	OpLen
	OpCast
	OpDeref
	OpIncrementIndex
	OpDecrementIndex
	OpIncrementCount
	OpGetIndex
	OpGetCount
	OpGetLen
	OpGetAcc
	OpSetAcc
	OpSetIndex
	OpPointer
	OpThrow
	OpCreate
	OpGroupBy
	OpSortBy
	OpSort
	OpProfileStart
	OpProfileEnd
	OpBegin
	OpEnd // This opcode must be at the end of this list.
)

func (op Opcode) String() string {
	switch op {
	case OpInvalid:
		return "OpInvalid"
	case OpPush:
		return "OpPush"
	case OpInt:
		return "OpInt"
	case OpPop:
		return "OpPop"
	case OpStore:
		return "OpStore"
	case OpLoadVar:
		return "OpLoadVar"
	case OpLoadConst:
		return "OpLoadConst"
	case OpLoadField:
		return "OpLoadField"
	case OpLoadFast:
		return "OpLoadFast"
	case OpLoadMethod:
		return "OpLoadMethod"
	case OpLoadFunc:
		return "OpLoadFunc"
	case OpLoadEnv:
		return "OpLoadEnv"
	case OpFetch:
		return "OpFetch"
	case OpFetchField:
		return "OpFetchField"
	case OpMethod:
		return "OpMethod"
	case OpTrue:
		return "OpTrue"
	case OpFalse:
		return "OpFalse"
	case OpNil:
		return "OpNil"
	case OpNegate:
		return "OpNegate"
	case OpNot:
		return "OpNot"
	case OpEqual:
		return "OpEqual"
	case OpEqualInt:
		return "OpEqualInt"
	case OpEqualString:
		return "OpEqualString"
	case OpJump:
		return "OpJump"
	case OpJumpIfTrue:
		return "OpJumpIfTrue"
	case OpJumpIfFalse:
		return "OpJumpIfFalse"
	case OpJumpIfNil:
		return "OpJumpIfNil"
	case OpJumpIfNotNil:
		return "OpJumpIfNotNil"
	case OpJumpIfEnd:
		return "OpJumpIfEnd"
	case OpJumpBackward:
		return "OpJumpBackward"
	case OpIn:
		return "OpIn"
	case OpLess:
		return "OpLess"
	case OpMore:
		return "OpMore"
	case OpLessOrEqual:
		return "OpLessOrEqual"
	case OpMoreOrEqual:
		return "OpMoreOrEqual"
	case OpAdd:
		return "OpAdd"
	case OpSubtract:
		return "OpSubtract"
	case OpMultiply:
		return "OpMultiply"
	case OpDivide:
		return "OpDivide"
	case OpModulo:
		return "OpModulo"
	case OpExponent:
		return "OpExponent"
	case OpRange:
		return "OpRange"
	case OpMatches:
		return "OpMatches"
	case OpMatchesConst:
		return "OpMatchesConst"
	case OpContains:
		return "OpContains"
	case OpStartsWith:
		return "OpStartsWith"
	case OpEndsWith:
		return "OpEndsWith"
	case OpSlice:
		return "OpSlice"
	case OpCall:
		return "OpCall"
	case OpCall0:
		return "OpCall0"
	case OpCall1:
		return "OpCall1"
	case OpCall2:
		return "OpCall2"
	case OpCall3:
		return "OpCall3"
	case OpCallN:
		return "OpCallN"
	case OpCallFast:
		return "OpCallFast"
	case OpCallSafe:
		return "OpCallSafe"
	case OpCallTyped:
		return "OpCallTyped"
	case OpCallBuiltin1:
		return "OpCallBuiltin1"
	case OpArray:
		return "OpArray"
	case OpMap:
		return "OpMap"
	case OpLen:
		return "OpLen"
	case OpCast:
		return "OpCast"
	case OpDeref:
		return "OpDeref"
	case OpIncrementIndex:
		return "OpIncrementIndex"
	case OpDecrementIndex:
		return "OpDecrementIndex"
	case OpIncrementCount:
		return "OpIncrementCount"
	case OpGetIndex:
		return "OpGetIndex"
	case OpGetCount:
		return "OpGetCount"
	case OpGetLen:
		return "OpGetLen"
	case OpGetAcc:
		return "OpGetAcc"
	case OpSetAcc:
		return "OpSetAcc"
	case OpSetIndex:
		return "OpSetIndex"
	case OpPointer:
		return "OpPointer"
	case OpThrow:
		return "OpThrow"
	case OpCreate:
		return "OpCreate"
	case OpGroupBy:
		return "OpGroupBy"
	case OpSortBy:
		return "OpSortBy"
	case OpSort:
		return "OpSort"
	case OpProfileStart:
		return "OpProfileStart"
	case OpProfileEnd:
		return "OpProfileEnd"
	case OpBegin:
		return "OpBegin"
	case OpEnd:
		return "OpEnd"
	default:
		return fmt.Sprintf("Opcode(%d)", byte(op))
	}
}
