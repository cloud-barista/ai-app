package model

import (
	"fmt"
	"strconv"
)

// Bounds of the limit every list takes.
const (
	// DefaultPageLimit is the limit of a list asked without one.
	DefaultPageLimit = 100
	// MaxPageLimit is the largest limit a list accepts.
	MaxPageLimit = 1000
)

// Page says which part of a list a response holds. A list is filtered first,
// then sorted in its own order, and the page is cut from that.
type Page struct {
	// Total is how many items the filters keep, before the page is cut.
	Total int `json:"total" example:"1"`
	// Limit is the most items one response holds.
	Limit int `json:"limit" example:"100"`
	// Offset is how many items of the list come before the first one returned.
	Offset int `json:"offset" example:"0"`
}

// NewPage checks a limit and an offset as a caller gave them; nil takes the
// default (limit 100, offset 0). The Page returned has no Total yet.
func NewPage(limit, offset *int) (Page, error) {
	p := Page{Limit: DefaultPageLimit}
	if limit != nil {
		if *limit < 1 || *limit > MaxPageLimit {
			return Page{}, errLimit()
		}
		p.Limit = *limit
	}
	if offset != nil {
		if *offset < 0 {
			return Page{}, errOffset()
		}
		p.Offset = *offset
	}
	return p, nil
}

// ParsePage reads a limit and an offset from query parameter text; an empty
// string takes the default.
func ParsePage(limit, offset string) (Page, error) {
	var l, o *int
	if limit != "" {
		n, err := strconv.Atoi(limit)
		if err != nil {
			return Page{}, errLimit()
		}
		l = &n
	}
	if offset != "" {
		n, err := strconv.Atoi(offset)
		if err != nil {
			return Page{}, errOffset()
		}
		o = &n
	}
	return NewPage(l, o)
}

// Paginate cuts the page p asks for out of items, kept in their order, and
// returns it with p and the total of items. An offset past the end gives an
// empty page.
func Paginate[T any](items []T, p Page) ([]T, Page) {
	p.Total = len(items)
	start := min(p.Offset, len(items))
	end := min(start+p.Limit, len(items))
	out := make([]T, 0, end-start)
	return append(out, items[start:end]...), p
}

func errLimit() error {
	return &ValidationError{Message: fmt.Sprintf("limit must be a whole number from 1 to %d", MaxPageLimit)}
}

func errOffset() error {
	return &ValidationError{Message: "offset must be a whole number, 0 or more"}
}
