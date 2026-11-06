package model

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestNewPageBounds(t *testing.T) {
	n := func(v int) *int { return &v }
	cases := []struct {
		limit, offset *int
		want          Page
		bad           string
	}{
		{nil, nil, Page{Limit: 100}, ""},
		{n(1), n(0), Page{Limit: 1}, ""},
		{n(1000), n(7), Page{Limit: 1000, Offset: 7}, ""},
		{n(0), nil, Page{}, "limit must be a whole number from 1 to 1000"},
		{n(-1), nil, Page{}, "limit must be a whole number from 1 to 1000"},
		{n(1001), nil, Page{}, "limit must be a whole number from 1 to 1000"},
		{nil, n(-1), Page{}, "offset must be a whole number, 0 or more"},
	}
	for _, tc := range cases {
		got, err := NewPage(tc.limit, tc.offset)
		if tc.bad == "" {
			if err != nil || got != tc.want {
				t.Errorf("NewPage(%v, %v) = %+v, %v, want %+v", tc.limit, tc.offset, got, err, tc.want)
			}
			continue
		}
		if !errors.Is(err, ErrValidation) || ValidationMessage(err) != tc.bad {
			t.Errorf("NewPage(%v, %v) err = %v, want %q", tc.limit, tc.offset, err, tc.bad)
		}
	}
}

func TestParsePage(t *testing.T) {
	if got, err := ParsePage("", ""); err != nil || got != (Page{Limit: DefaultPageLimit}) {
		t.Errorf(`ParsePage("", "") = %+v, %v`, got, err)
	}
	if got, err := ParsePage("2", "1"); err != nil || got != (Page{Limit: 2, Offset: 1}) {
		t.Errorf(`ParsePage("2", "1") = %+v, %v`, got, err)
	}
	for _, in := range [][2]string{{"x", ""}, {"1.5", ""}, {"0", ""}, {"", "x"}, {"", "-3"}} {
		if _, err := ParsePage(in[0], in[1]); !errors.Is(err, ErrValidation) {
			t.Errorf("ParsePage(%q, %q) err = %v, want a validation error", in[0], in[1], err)
		}
	}
}

func TestPaginate(t *testing.T) {
	items := []string{"a", "b", "c", "d", "e"}
	cases := []struct {
		page Page
		want []string
	}{
		{Page{Limit: 100}, items},
		{Page{Limit: 2}, []string{"a", "b"}},
		{Page{Limit: 2, Offset: 1}, []string{"b", "c"}},
		{Page{Limit: 10, Offset: 4}, []string{"e"}},
		{Page{Limit: 10, Offset: 5}, []string{}},
		{Page{Limit: 10, Offset: 50}, []string{}},
	}
	for _, tc := range cases {
		got, p := Paginate(items, tc.page)
		if got == nil || !slices.Equal(got, tc.want) || p.Total != 5 || p.Limit != tc.page.Limit || p.Offset != tc.page.Offset {
			t.Errorf("Paginate(%+v) = %v, %+v, want %v with total 5", tc.page, got, p, tc.want)
		}
	}
	got, _ := Paginate(items, Page{Limit: 2})
	got[0] = "changed"
	if items[0] != "a" {
		t.Errorf("Paginate shares its result with the input: %s", strings.Join(items, ","))
	}
	if got, p := Paginate([]int(nil), Page{Limit: 3}); got == nil || len(got) != 0 || p.Total != 0 {
		t.Errorf("Paginate(nil) = %v, %+v, want an empty list", got, p)
	}
}
