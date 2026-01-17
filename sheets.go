package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"
)

type Categories struct {
	Spenders []string
	Cats     []string
	Cards    []string
}

type SheetsClient struct {
	srv          *sheets.Service
	spreadsheet  string
	cacheMu      sync.Mutex
	cache        Categories
	cacheExpires time.Time
	cacheTTL     time.Duration
}

func NewSheetsClient(ctx context.Context, spreadsheetID string, opts ...option.ClientOption) (*SheetsClient, error) {
	srv, err := sheets.NewService(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return &SheetsClient{
		srv:         srv,
		spreadsheet: spreadsheetID,
		cacheTTL:    5 * time.Minute,
	}, nil
}

func (c *SheetsClient) GetCategories(ctx context.Context) (Categories, error) {
	c.cacheMu.Lock()
	if time.Now().Before(c.cacheExpires) && (len(c.cache.Cats)+len(c.cache.Spenders)+len(c.cache.Cards) > 0) {
		defer c.cacheMu.Unlock()
		return c.cache, nil
	}
	c.cacheMu.Unlock()

	resp, err := c.srv.Spreadsheets.Values.BatchGet(c.spreadsheet).
		Ranges("Категории!A:A", "Категории!B:B", "Категории!D:D").
		MajorDimension("COLUMNS").
		Context(ctx).
		Do()
	if err != nil {
		return Categories{}, err
	}

	var out Categories
	for _, vr := range resp.ValueRanges {
		r := strings.ReplaceAll(vr.Range, "'", "")

		switch {
		case strings.HasPrefix(r, "Категории!A"):
			out.Cats = normalizeColumn(vr.Values)
		case strings.HasPrefix(r, "Категории!B"):
			out.Spenders = normalizeColumn(vr.Values)
		case strings.HasPrefix(r, "Категории!D"):
			out.Cards = normalizeColumn(vr.Values)
		}
	}

	c.cacheMu.Lock()
	c.cache = out
	c.cacheExpires = time.Now().Add(c.cacheTTL)
	c.cacheMu.Unlock()

	return out, nil
}

func normalizeColumn(values [][]interface{}) []string {
	if len(values) == 0 {
		return nil
	}
	col := values[0]
	res := make([]string, 0, len(col))
	for _, v := range col {
		s := strings.TrimSpace(fmt.Sprint(v))
		if s == "" {
			continue
		}
		res = append(res, s)
	}
	return res
}

func (c *SheetsClient) AppendExpenseRow(ctx context.Context, date, spender, category string, amount int, card, comment string) error {
	t, err := time.Parse("02.01.2006", date)
	if err != nil {
		return err
	}
	month := int(t.Month())

	row := []interface{}{date, spender, category, amount, comment, card, month}

	vr := &sheets.ValueRange{
		Values: [][]interface{}{row},
	}

	_, err = c.srv.Spreadsheets.Values.Append(
		c.spreadsheet,
		"Расходы!A:F",
		vr,
	).
		ValueInputOption("USER_ENTERED").
		InsertDataOption("INSERT_ROWS").
		Context(ctx).
		Do()

	return err
}
