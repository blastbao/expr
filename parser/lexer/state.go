package lexer

import (
	"strings"

	"github.com/expr-lang/expr/parser/utils"
)

type stateFn func(*lexer) stateFn

// root 逐字符扫描源代码，并根据字符的含义进入不同状态函数或直接生成 Token 。
// stateFn 是函数类型，表示下一个状态。可以返回自身（root）、另一个状态函数（如 number）、或 nil（表示终止扫描）。
func root(l *lexer) stateFn {
	// 读取一个字符（rune）
	switch r := l.next(); {
	case r == eof:
		// 如果读到 eof（-1），说明已经扫描完毕。
		// 调用 emitEOF() 生成一个 EOF Token，返回 nil 表示扫描终止。
		l.emitEOF()
		return nil
	case utils.IsSpace(r):
		// 如果是空格、制表符等跳过字符，调用 skip() 跳过所有空白字符。
		// 继续保持在 root 状态。
		l.skip()
		return root
	case r == '\'' || r == '"':
		// 如果是 ' 或 "，说明是一个转义字符串。
		//	- scanString(r)：扫描字符串直到闭合引号。
		//	- unescape(...)：将转义字符（如 \n）转换为实际字符。
		// 生成 String 类型的 Token。
		l.scanString(r)
		str, err := unescape(l.word())
		if err != nil {
			l.error("%v", err)
		}
		l.emitValue(String, str)
	case r == '`':
		// 如果是反引号（Go 风格），就是原始字符串。
		// 不处理转义字符，原样提取内容。
		l.scanRawString(r)
	case '0' <= r && r <= '9':
		// 遇到数字，进入 number 状态。
		// 先 backup() 回退当前字符，留给 number 状态函数完整处理整个数字。
		l.backup()
		return number
	case r == '?':
		return questionMark
	case r == '/':
		return slash
	case r == '#':
		return pointer
	case r == '|':
		l.accept("|")
		l.emit(Operator)
	case r == ':':
		l.accept(":")
		l.emit(Operator)
	case strings.ContainsRune("([{", r):
		l.emit(Bracket)
	case strings.ContainsRune(")]}", r):
		l.emit(Bracket)
	case strings.ContainsRune(",;%+-^", r): // single rune operator
		l.emit(Operator)
	case strings.ContainsRune("&!=*<>", r): // possible double rune operator
		l.accept("&=*")
		l.emit(Operator)
	case r == '.':
		// . 有可能是：
		//	- 小数点（3.14）→ 属于数字
		//	- 范围运算符（1..5）
		//	- 属性访问（a.b）
		// 所以回退一个字符，让 dot 状态来进一步判断。
		l.backup()
		return dot
	case utils.IsAlphaNumeric(r):
		// 如果是字母或数字（开头必须是字母），则回退一个字符，进入 identifier 状态，去识别变量名、关键字等。
		l.backup()
		return identifier
	default:
		return l.error("unrecognized character: %#U", r)
	}
	return root
}

func number(l *lexer) stateFn {
	if !l.scanNumber() {
		return l.error("bad number syntax: %q", l.word())
	}
	l.emit(Number)
	return root
}

func (l *lexer) scanNumber() bool {
	digits := "0123456789_"
	// Is it hex?
	if l.accept("0") {
		// Note: Leading 0 does not mean octal in floats.
		if l.accept("xX") {
			digits = "0123456789abcdefABCDEF_"
		} else if l.accept("oO") {
			digits = "01234567_"
		} else if l.accept("bB") {
			digits = "01_"
		}
	}
	l.acceptRun(digits)
	end := l.end
	if l.accept(".") {
		// Lookup for .. operator: if after dot there is another dot (1..2), it maybe a range operator.
		if l.peek() == '.' {
			// We can't backup() here, as it would require two backups,
			// and backup() func supports only one for now. So, save and
			// restore it here.
			l.end = end
			return true
		}
		l.acceptRun(digits)
	}
	if l.accept("eE") {
		l.accept("+-")
		l.acceptRun(digits)
	}
	// Next thing mustn't be alphanumeric.
	if utils.IsAlphaNumeric(l.peek()) {
		l.next()
		return false
	}
	return true
}

func dot(l *lexer) stateFn {
	l.next()
	if l.accept("0123456789") {
		l.backup()
		return number
	}
	l.accept(".")
	l.emit(Operator)
	return root
}

func identifier(l *lexer) stateFn {
loop:
	for {
		switch r := l.next(); {
		case utils.IsAlphaNumeric(r):
			// absorb
		default:
			l.backup()
			switch l.word() {
			case "not":
				return not
			case "in", "or", "and", "matches", "contains", "startsWith", "endsWith", "let", "if", "else":
				l.emit(Operator)
			default:
				l.emit(Identifier)
			}
			break loop
		}
	}
	return root
}

func not(l *lexer) stateFn {
	l.emit(Operator)

	l.skipSpaces()

	end := l.end

	// Get the next word.
	for {
		r := l.next()
		if utils.IsAlphaNumeric(r) {
			// absorb
		} else {
			l.backup()
			break
		}
	}

	switch l.word() {
	case "in", "matches", "contains", "startsWith", "endsWith":
		l.emit(Operator)
	default:
		l.end = end
	}
	return root
}

func questionMark(l *lexer) stateFn {
	l.accept(".?")
	l.emit(Operator)
	return root
}

func slash(l *lexer) stateFn {
	if l.accept("/") {
		return singleLineComment
	}
	if l.accept("*") {
		return multiLineComment
	}
	l.emit(Operator)
	return root
}

func singleLineComment(l *lexer) stateFn {
	for {
		r := l.next()
		if r == eof || r == '\n' {
			break
		}
	}
	l.skip()
	return root
}

func multiLineComment(l *lexer) stateFn {
	for {
		r := l.next()
		if r == eof {
			return l.error("unclosed comment")
		}
		if r == '*' && l.accept("/") {
			break
		}
	}
	l.skip()
	return root
}

func pointer(l *lexer) stateFn {
	l.accept("#")
	l.emit(Operator)
	for {
		switch r := l.next(); {
		case utils.IsAlphaNumeric(r): // absorb
		default:
			l.backup()
			if l.word() != "" {
				l.emit(Identifier)
			}
			return root
		}
	}
}
