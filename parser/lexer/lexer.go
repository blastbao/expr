package lexer

import (
	"fmt"
	"strings"

	"github.com/expr-lang/expr/file"
)

func Lex(source file.Source) ([]Token, error) {
	l := &lexer{
		source: source,
		tokens: make([]Token, 0),
		start:  0,
		end:    0,
	}
	l.commit()

	for state := root; state != nil; {
		state = state(l)
	}

	if l.err != nil {
		return nil, l.err.Bind(source)
	}

	return l.tokens, nil
}

type lexer struct {
	source     file.Source
	tokens     []Token
	start, end int
	err        *file.Error
}

const eof rune = -1

func (l *lexer) commit() {
	l.start = l.end
}

func (l *lexer) next() rune {
	if l.end >= len(l.source) {
		l.end++
		return eof
	}
	r := l.source[l.end]
	l.end++
	return r
}

func (l *lexer) peek() rune {
	r := l.next()
	l.backup()
	return r
}

func (l *lexer) backup() {
	l.end--
}

func (l *lexer) emit(t Kind) {
	l.emitValue(t, l.word())
}

// 构造一个 Token 实例，并追加到 l.tokens 切片中。
func (l *lexer) emitValue(t Kind, value string) {
	l.tokens = append(l.tokens, Token{
		Location: file.Location{From: l.start, To: l.end}, // 记录 token 在源码中的位置，用于错误定位、调试
		Kind:     t,                                       // 标识 token 类型
		Value:    value,                                   // 存储真正的 token 字符串
	})
	l.commit()
}

func (l *lexer) emitEOF() {
	from := l.end - 2
	if from < 0 {
		from = 0
	}
	to := l.end - 1
	if to < 0 {
		to = 0
	}

	l.tokens = append(l.tokens, Token{
		Location: file.Location{From: from, To: to},
		Kind:     EOF,
	})
	l.commit()
}

func (l *lexer) skip() {
	l.commit()
}

func (l *lexer) word() string {
	// TODO: boundary check is NOT needed here, but for some reason CI fuzz tests are failing.
	// l.start 和 l.end 应该始终在合法范围内
	if l.start > len(l.source) || l.end > len(l.source) {
		return "__invalid__"
	}
	// 取 [start:end] 区间内容作为当前 word
	return string(l.source[l.start:l.end])
}

func (l *lexer) accept(valid string) bool {
	// 1) 读取下一个字符 r ，并移动 end++
	// 2) 检查字符 r 是否存在于 valid 中
	if strings.ContainsRune(valid, l.next()) {
		return true
	}
	// 如果 r 不在 valid 中，回退 end--
	l.backup()
	return false
}

func (l *lexer) acceptRun(valid string) {
	for strings.ContainsRune(valid, l.next()) {
	}
	l.backup()
}

func (l *lexer) skipSpaces() {
	r := l.peek()
	for ; r == ' '; r = l.peek() {
		l.next()
	}
	l.skip()
}

func (l *lexer) acceptWord(word string) bool {
	pos := l.end

	// 跳过所有前导空白字符
	l.skipSpaces()

	// 检查后续字符是否和 word 匹配，不匹配直接回滚并返回
	for _, ch := range word {
		if l.next() != ch {
			l.end = pos // 匹配失败时完全回退，不改变 lexer 状态
			return false
		}
	}

	// 确保完整匹配而非子串
	if r := l.peek(); r != ' ' && r != eof {
		l.end = pos
		return false
	}

	return true
}

func (l *lexer) error(format string, args ...any) stateFn {
	if l.err == nil { // show first error
		l.err = &file.Error{
			Location: file.Location{
				From: l.end - 1,
				To:   l.end,
			},
			Message: fmt.Sprintf(format, args...),
		}
	}
	return nil
}

func digitVal(ch rune) int {
	switch {
	case '0' <= ch && ch <= '9':
		return int(ch - '0')
	case 'a' <= lower(ch) && lower(ch) <= 'f':
		return int(lower(ch) - 'a' + 10)
	}
	return 16 // larger than any legal digit val
}

func lower(ch rune) rune { return ('a' - 'A') | ch } // returns lower-case ch iff ch is ASCII letter

func (l *lexer) scanDigits(ch rune, base, n int) rune {
	for n > 0 && digitVal(ch) < base {
		ch = l.next()
		n--
	}
	if n > 0 {
		l.error("invalid char escape")
	}
	return ch
}

// 在扫描字符串时（如 "abc\n123"）遇到反斜杠 \ 时被调用，用于解析各种合法的转义序列，如：
//	- \n（换行）
//	- \\（反斜杠）
//	- \x20（十六进制）
//	- \u1234（Unicode）
// 	- \U0010FFFF（Unicode）

// 常规转义字符：
//	\a - 响铃
//	\b - 退格
//	\f - 换页
//	\n - 换行
//	\r - 回车
//	\t - 水平制表符
//	\v - 垂直制表符
//	\\ - 反斜杠本身
// 这些不需要特殊处理，只要跳过就好。

// 八进制转义：
//	\000 ～ \777
// 读取最多 3 个八进制数（0~7），如 \141 表示 a 。

// 十六进制转义：
//	\xFF
// 读取 x 后的 2 个十六进制数字（如 \x41 表示 A）。

// Unicode 短格式：
//	\u1234
// 读取 u 后 4 个 hex（如 \u4E2D = "中"）。

// Unicode 长格式：
//	\U0001F600
// 读取 U 后 8 个 hex（如 \U0001F600 表情😀）。

// 示例
//
// 处理字符串 "a\\b\x41\u4e2d"：
//   - a - 普通字符
//   - \\ - 转义为反斜杠
//   - b - 普通字符
//   - \x41 - 转义为字母'A'
//   - \u4e2d - 转义为中文"中"
//
// 最终字符串值为：a\bA中
func (l *lexer) scanEscape(quote rune) rune {
	ch := l.next() // read character after '/'
	switch ch {
	case 'a', 'b', 'f', 'n', 'r', 't', 'v', '\\', quote:
		// 简单转义序列 nothing to do
		ch = l.next()
	case '0', '1', '2', '3', '4', '5', '6', '7':
		// 八进制
		ch = l.scanDigits(ch, 8, 3)
	case 'x':
		// 十六进制
		ch = l.scanDigits(l.next(), 16, 2)
	case 'u':
		// 4 位十六进制 Unicode
		ch = l.scanDigits(l.next(), 16, 4)
	case 'U':
		// 8 位十六进制 Unicode
		ch = l.scanDigits(l.next(), 16, 8)
	default:
		l.error("invalid char escape")
	}
	return ch
}

func (l *lexer) scanString(quote rune) (n int) {
	// 读取引号后的第一个字符
	ch := l.next() // read character after quote
	// 进入循环，直到遇到匹配的结束引号
	for ch != quote {
		// 如果遇到换行符或文件结束符，表示字符串未正确终止
		if ch == '\n' || ch == eof {
			l.error("literal not terminated")
			return
		}

		// 遇到反斜杠 \ 时，调用 scanEscape 处理转义序列，返回转义处理后的下一个字符
		if ch == '\\' {
			ch = l.scanEscape(quote)
		} else {
			// 普通字符则直接读取下一个字符
			ch = l.next()
		}

		// 计数
		n++
	}
	return
}

func (l *lexer) scanRawString(quote rune) (n int) {
	ch := l.next() // read character after back tick
	for ch != quote {
		if ch == eof {
			l.error("literal not terminated")
			return
		}
		ch = l.next()
		n++
	}
	l.emitValue(String, string(l.source[l.start+1:l.end-1]))
	return
}
