package operator

// Associativity 运算符结合性
type Associativity int

const (
	Left  Associativity = iota + 1 // 从左到右计算（左结合）
	Right                          // 从右到左计算（右结合）
)

// Operator 运算符属性
type Operator struct {
	Precedence    int           // 优先级，越大优先级越高
	Associativity Associativity // 结合性
}

// Less 比较两个运算符的优先级，如果 a 的优先级低于 b 则返回 true ，用于确定运算顺序
func Less(a, b string) bool {
	return Binary[a].Precedence < Binary[b].Precedence
}

// IsBoolean 判断是否是布尔运算符
func IsBoolean(op string) bool {
	return op == "and" || op == "or" || op == "&&" || op == "||"
}

// AllowedNegateSuffix 判断哪些运算符可以加否定后缀
func AllowedNegateSuffix(op string) bool {
	switch op {
	case "contains", "matches", "startsWith", "endsWith", "in":
		return true
	default:
		return false
	}
}

// 运算符优先级规则：
//	- 逻辑运算符优先级最低
//	- 算术运算符中等优先级
//	- 幂运算优先级最高
//	- 大多数运算符是左结合的，除了幂运算是右结合的

// Unary 一元运算符
var Unary = map[string]Operator{
	"not": {50, Left},
	"!":   {50, Left},
	"-":   {90, Left},
	"+":   {90, Left},
}

// Binary 二元运算符
var Binary = map[string]Operator{
	"|":          {0, Left},
	"or":         {10, Left},
	"||":         {10, Left},
	"and":        {15, Left},
	"&&":         {15, Left},
	"==":         {20, Left},
	"!=":         {20, Left},
	"<":          {20, Left},
	">":          {20, Left},
	">=":         {20, Left},
	"<=":         {20, Left},
	"in":         {20, Left},
	"matches":    {20, Left},
	"contains":   {20, Left},
	"startsWith": {20, Left},
	"endsWith":   {20, Left},
	"..":         {25, Left},
	"+":          {30, Left},
	"-":          {30, Left},
	"*":          {60, Left},
	"/":          {60, Left},
	"%":          {60, Left},
	"**":         {100, Right},
	"^":          {100, Right},
	"??":         {500, Left},
}

// IsComparison 判断是否是比较运算符
func IsComparison(op string) bool {
	return op == "<" || op == ">" || op == ">=" || op == "<="
}
