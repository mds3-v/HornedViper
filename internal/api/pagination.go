package api

import (
	"net/http"
	"strconv"
)

type PageLink struct {
	Number int
	URL    string
	Active bool
}

type PageInfo struct {
	Page       int
	PerPage    int
	Total      int
	TotalPages int
	From       int
	To         int
	HasPrev    bool
	HasNext    bool
	PrevURL    string
	NextURL    string
	Links      []PageLink
}

func newPageInfo(r *http.Request, total int) PageInfo {
	return newPageInfoFor(r, total, "page")
}

func newPageInfoFor(r *http.Request, total int, param string) PageInfo {
	perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if perPage <= 0 {
		perPage = 10
	}
	if perPage > 100 {
		perPage = 100
	}
	page, _ := strconv.Atoi(r.URL.Query().Get(param))
	if page <= 0 {
		page = 1
	}
	totalPages := (total + perPage - 1) / perPage
	if totalPages == 0 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}
	urlFor := func(p int) string {
		q := r.URL.Query()
		q.Set(param, strconv.Itoa(p))
		return r.URL.Path + "?" + q.Encode()
	}
	pi := PageInfo{
		Page: page, PerPage: perPage, Total: total, TotalPages: totalPages,
		HasPrev: page > 1, HasNext: page < totalPages,
		PrevURL: urlFor(page - 1), NextURL: urlFor(page + 1),
	}
	if total > 0 {
		pi.From = (page-1)*perPage + 1
		pi.To = pi.From + perPage - 1
		if pi.To > total {
			pi.To = total
		}
	}
	start, end := page-2, page+2
	if start < 1 {
		start = 1
	}
	if end > totalPages {
		end = totalPages
	}
	if totalPages > 7 {
		if start > 2 {
			pi.Links = append(pi.Links, PageLink{Number: 1, URL: urlFor(1)})
			if start > 3 {
				pi.Links = append(pi.Links, PageLink{Number: 0})
			}
		}
	}
	for p := start; p <= end; p++ {
		pi.Links = append(pi.Links, PageLink{Number: p, URL: urlFor(p), Active: p == page})
	}
	if totalPages > 7 {
		if end < totalPages-1 {
			if end < totalPages-2 {
				pi.Links = append(pi.Links, PageLink{Number: 0})
			}
			pi.Links = append(pi.Links, PageLink{Number: totalPages, URL: urlFor(totalPages)})
		}
	}
	return pi
}

func paginateSlice[T any](items []T, p PageInfo) []T {
	start := (p.Page - 1) * p.PerPage
	if start >= len(items) {
		return nil
	}
	end := start + p.PerPage
	if end > len(items) {
		end = len(items)
	}
	return items[start:end]
}

// newPageInfoSize is newPageInfoFor with an explicit per-page size.
func newPageInfoSize(r *http.Request, total int, param string, size int) PageInfo {
	q := r.URL.Query()
	q.Set("per_page", strconv.Itoa(size))
	r2 := new(http.Request)
	*r2 = *r
	u := *r.URL
	u.RawQuery = q.Encode()
	r2.URL = &u
	return newPageInfoFor(r2, total, param)
}
