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

const (
	sheetCategories = "Категории"
	sheetExpenses   = "Расходы"

	rangeCatNames       = sheetCategories + "!A:A"
	rangeSpenders       = sheetCategories + "!B:B"
	rangePrefixCatNames = sheetCategories + "!A"
	rangePrefixSpenders = sheetCategories + "!B"
	rangeExpensesAppend = sheetExpenses + "!A:F"
)

// Categories содержит имена пользаков и названия категорий расходов из таблицы.
type Categories struct {
	Spenders []string
	Names    []string
}

// SheetsClient оборачивает Google Sheets API и кеширует данные категорий.
type SheetsClient struct {
	srv          *sheets.Service
	spreadsheet  string
	cacheMu      sync.Mutex
	cache        Categories
	cacheExpires time.Time
	cacheTTL     time.Duration
}

// NewSheetsClient создает SheetsClient для работы с указанной таблицей.
func NewSheetsClient(ctx context.Context, spreadsheetID string, opts ...option.ClientOption) (*SheetsClient, error) {
	srv, err := sheets.NewService(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("creating sheets service: %w", err)
	}
	return &SheetsClient{
		srv:         srv,
		spreadsheet: spreadsheetID,
		cacheTTL:    5 * time.Minute,
	}, nil
}

// GetCategories возвращает плательщиков и категории с кешированием на 5 минут.
func (c *SheetsClient) GetCategories(ctx context.Context) (Categories, error) {
	c.cacheMu.Lock()
	if time.Now().Before(c.cacheExpires) && len(c.cache.Names)+len(c.cache.Spenders) > 0 {
		cached := c.cache
		c.cacheMu.Unlock()
		return cached, nil
	}
	c.cacheMu.Unlock()

	resp, err := c.srv.Spreadsheets.Values.BatchGet(c.spreadsheet).
		Ranges(rangeSpenders, rangeCatNames).
		MajorDimension("COLUMNS").
		Context(ctx).
		Do()
	if err != nil {
		return Categories{}, fmt.Errorf("fetching categories: %w", err)
	}

	var out Categories
	for _, vr := range resp.ValueRanges {
		r := strings.ReplaceAll(vr.Range, "'", "")

		switch {
		case strings.HasPrefix(r, rangePrefixCatNames):
			out.Names = normalizeColumn(vr.Values)
		case strings.HasPrefix(r, rangePrefixSpenders):
			out.Spenders = normalizeColumn(vr.Values)
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

// AppendExpenseRow добавляет одну запись расхода в лист «Расходы».
func (c *SheetsClient) AppendExpenseRow(ctx context.Context, date, spender, category string, amount int, submitter, comment string) error {
	t, err := time.Parse(dateFmt, date)
	if err != nil {
		return fmt.Errorf("parsing expense date %q: %w", date, err)
	}
	month := int(t.Month())

	row := []interface{}{date, spender, category, amount, comment, submitter, month}

	vr := &sheets.ValueRange{
		Values: [][]interface{}{row},
	}

	_, err = c.srv.Spreadsheets.Values.Append(
		c.spreadsheet,
		rangeExpensesAppend,
		vr,
	).
		ValueInputOption("USER_ENTERED").
		InsertDataOption("INSERT_ROWS").
		Context(ctx).
		Do()
	if err != nil {
		return fmt.Errorf("appending expense row: %w", err)
	}

	return nil
}
