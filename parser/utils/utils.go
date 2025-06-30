package utils

import (
	"unicode"
	"unicode/utf8"
)

// IsValidIdentifier 验证字符串是否为有效的标识符
//
// 规则：
//   - 不能为空字符串
//   - 首字符必须是字母、下划线_或美元符号$
//   - 后续字符可以是字母、数字、下划线_或美元符号$
//
// 逻辑：
//   - 空字符串直接返回 false
//   - 使用 utf8.DecodeRuneInString 解码第一个字符
//   - 检查首字符是否符合 IsAlphabetic 规则
//   - 遍历剩余字符，检查是否符合 IsAlphaNumeric 规则
func IsValidIdentifier(str string) bool {
	if len(str) == 0 {
		return false
	}
	h, w := utf8.DecodeRuneInString(str)
	if !IsAlphabetic(h) {
		return false
	}
	for _, r := range str[w:] {
		if !IsAlphaNumeric(r) {
			return false
		}
	}
	return true
}

// IsSpace 是否为空白字符
func IsSpace(r rune) bool {
	return unicode.IsSpace(r)
}

// IsAlphaNumeric 是否为字母或数字
func IsAlphaNumeric(r rune) bool {
	return IsAlphabetic(r) || unicode.IsDigit(r)
}

// IsAlphabetic 是否为有效字母字符，允许 _、$ 或任何 Unicode 字母
func IsAlphabetic(r rune) bool {
	return r == '_' || r == '$' || unicode.IsLetter(r)
}
