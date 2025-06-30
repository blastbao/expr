package lexer

import (
	"fmt"

	"github.com/expr-lang/expr/file"
)

// Kind 表示 token 类型
type Kind string

const (
	Identifier Kind = "Identifier" // 标识符（变量名、函数名等）
	Number     Kind = "Number"     // 数字字面量
	String     Kind = "String"     // 字符串字面量
	Operator   Kind = "Operator"   // 运算符（+、-、*等）
	Bracket    Kind = "Bracket"    // 括号（()、[]、{}等）
	EOF        Kind = "EOF"        // 文件结束标记
)

type Token struct {
	file.Location        // token 在源码的位置
	Kind          Kind   // 类型
	Value         string // 值
}

// String 将 Token 格式化为可读字符串：
//   - Token{Kind: Identifier, Value: "foo"}.String() // 输出: Identifier("foo")
//   - Token{Kind: EOF}.String()                      // 输出: EOF
func (t Token) String() string {
	// 当 Value 为空时，返回 Kind
	if t.Value == "" {
		return string(t.Kind)
	}
	// 当 Value 非空时，返回 Kind(Value)
	return fmt.Sprintf("%s(%#v)", t.Kind, t.Value)
}

func (t Token) Is(kind Kind, values ...string) bool {
	if len(values) == 0 {
		return kind == t.Kind
	}

	for _, v := range values {
		if v == t.Value {
			goto found
		}
	}
	return false

found:
	return kind == t.Kind
}
