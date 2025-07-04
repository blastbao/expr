package file

import "fmt"

type Location struct {
	From int `json:"from"`
	To   int `json:"to"`
}

func (loc Location) String() string {
	return fmt.Sprintf("[%d:%d]", loc.From, loc.To)
}
