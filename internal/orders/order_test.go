package orders

import (
	"errors"
	"testing"
)

func TestNext(t *testing.T) {
	cases := []struct {
		from   Status
		action string
		want   Status // "" means not allowed
	}{
		{Pending, "pay", Paid},
		{Paid, "ship", Shipped},
		{Pending, "cancel", Cancelled},
		{Paid, "cancel", Cancelled},
		{Pending, "ship", ""},
		{Shipped, "cancel", ""},
		{Cancelled, "pay", ""},
		{Cancelled, "cancel", ""},
	}
	for _, tc := range cases {
		got, err := Next(tc.from, tc.action)
		if tc.want == "" {
			if !errors.Is(err, ErrBadTransition) {
				t.Errorf("%s from %s: got %q, %v; want ErrBadTransition", tc.action, tc.from, got, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%s from %s = %q, %v; want %q", tc.action, tc.from, got, err, tc.want)
		}
	}
	if _, err := Next(Pending, "refund"); err == nil {
		t.Error("unknown action accepted")
	}
}

func TestNormalizeMergesAndSorts(t *testing.T) {
	r := CreateRequest{CustomerID: 1, Items: []Item{{ProductID: 9, Qty: 1}, {ProductID: 2, Qty: 2}, {ProductID: 9, Qty: 3}}}
	if err := r.Normalize(); err != nil {
		t.Fatal(err)
	}
	if len(r.Items) != 2 || r.Items[0] != (Item{ProductID: 2, Qty: 2}) || r.Items[1] != (Item{ProductID: 9, Qty: 4}) {
		t.Fatalf("items = %+v", r.Items)
	}

	// Equal requests hash equal regardless of line order or splitting.
	s := CreateRequest{CustomerID: 1, Items: []Item{{ProductID: 9, Qty: 4}, {ProductID: 2, Qty: 2}}}
	s.Normalize()
	if r.Hash() != s.Hash() {
		t.Error("equivalent requests hash differently")
	}

	over := CreateRequest{CustomerID: 1, Items: []Item{{ProductID: 1, Qty: 600}, {ProductID: 1, Qty: 600}}}
	if over.Normalize() == nil {
		t.Error("merged qty over the limit accepted")
	}
}
