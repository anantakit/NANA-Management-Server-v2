package dto

import (
	"math"
	"slices"
)

type PaginationParams struct {
	Page   int    `query:"page"`
	Limit  int    `query:"limit"`
	Sort   string `query:"sort"`
	Order  string `query:"order"`
	Search string `query:"search"`
}

func (p *PaginationParams) Normalize() {
	if p.Page < 1 {
		p.Page = 1
	}
	if p.Limit < 1 {
		p.Limit = 20
	}
	if p.Limit > 100 {
		p.Limit = 100
	}
	if p.Order != "asc" && p.Order != "desc" {
		p.Order = "desc"
	}
}

func (p *PaginationParams) Offset() int {
	return (p.Page - 1) * p.Limit
}

type Meta struct {
	Page       int   `json:"page"`
	Limit      int   `json:"limit"`
	Total      int64 `json:"total"`
	TotalPages int   `json:"total_pages"`
}

func ComputeMeta(page, limit int, total int64) Meta {
	totalPages := int(math.Ceil(float64(total) / float64(limit)))
	if totalPages < 1 {
		totalPages = 1
	}
	return Meta{
		Page:       page,
		Limit:      limit,
		Total:      total,
		TotalPages: totalPages,
	}
}

func SafeSort(col, order string, allowed []string, defaultCol string) (string, string) {
	if !slices.Contains(allowed, col) {
		col = defaultCol
	}
	if order != "asc" && order != "desc" {
		order = "desc"
	}
	return col, order
}
