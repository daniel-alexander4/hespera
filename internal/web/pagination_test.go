package web

import (
	"net/http/httptest"
	"testing"
)

func TestPageParam(t *testing.T) {
	cases := map[string]int{
		"":          1,
		"?page=0":   1,
		"?page=1":   1,
		"?page=3":   3,
		"?page=-2":  1,
		"?page=abc": 1,
	}
	for q, want := range cases {
		r := httptest.NewRequest("GET", "/music/albums"+q, nil)
		if got := pageParam(r); got != want {
			t.Errorf("pageParam(%q) = %d, want %d", q, got, want)
		}
	}
}

func TestPaginate(t *testing.T) {
	// total 150, page size 60 → 3 pages.
	t.Run("middle page", func(t *testing.T) {
		nav, offset := paginate(2, 150, "/music/albums")
		if nav.TotalPages != 3 {
			t.Fatalf("TotalPages = %d, want 3", nav.TotalPages)
		}
		if offset != 60 {
			t.Fatalf("offset = %d, want 60", offset)
		}
		if !nav.HasPrev || !nav.HasNext {
			t.Fatalf("middle page should have both prev and next: %+v", nav)
		}
		if nav.PrevPage != 1 || nav.NextPage != 3 {
			t.Fatalf("prev/next = %d/%d, want 1/3", nav.PrevPage, nav.NextPage)
		}
	})

	t.Run("page clamped past the end", func(t *testing.T) {
		nav, offset := paginate(99, 150, "/x")
		if nav.Page != 3 {
			t.Fatalf("page = %d, want clamped to 3", nav.Page)
		}
		if offset != 120 {
			t.Fatalf("offset = %d, want 120 (page 3)", offset)
		}
		if nav.HasNext {
			t.Fatal("last page must not have next")
		}
	})

	t.Run("empty set is one page", func(t *testing.T) {
		nav, offset := paginate(1, 0, "/x")
		if nav.TotalPages != 1 || nav.Page != 1 || offset != 0 {
			t.Fatalf("empty set: %+v offset=%d, want 1 page", nav, offset)
		}
		if nav.HasPrev || nav.HasNext {
			t.Fatal("single page has no prev/next")
		}
	})

	t.Run("exact multiple", func(t *testing.T) {
		nav, _ := paginate(1, 120, "/x")
		if nav.TotalPages != 2 {
			t.Fatalf("TotalPages = %d, want 2 (120/60)", nav.TotalPages)
		}
	})
}
