package options

import (
	"fmt"

	"github.com/bresilla/bin/src/pkg/ui"
)

type LiteralStringer string

func (l LiteralStringer) String() string {
	return string(l)
}

// Select prompts the user to choose one of the available options and returns
// the selected one.
func Select(msg string, opts []fmt.Stringer) (interface{}, error) {
	if len(opts) == 1 {
		return opts[0], nil
	}
	items := make([]string, len(opts))
	for i, o := range opts {
		items[i] = o.String()
	}
	idx, err := ui.SelectOne(msg, items)
	if err != nil {
		return nil, err
	}
	return opts[idx], nil
}

// SelectCustom prompts the user to choose one of the available options or type
// a custom value, returning the selected option or a LiteralStringer.
func SelectCustom(msg string, opts []fmt.Stringer) (interface{}, error) {
	if len(opts) == 1 {
		return opts[0], nil
	}
	items := make([]string, len(opts))
	for i, o := range opts {
		items[i] = o.String()
	}
	v, err := ui.SelectOrInput(msg, items)
	if err != nil {
		return nil, err
	}
	for i, it := range items {
		if it == v {
			return opts[i], nil
		}
	}
	return LiteralStringer(v), nil
}
