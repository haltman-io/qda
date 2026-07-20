package output

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"qda/internal/types"
)

// JSONLWriter streams results as JSON lines.
type JSONLWriter struct {
	mu      sync.Mutex
	encoder *json.Encoder
}

// NewJSONLWriter creates a streaming JSON-lines writer.
func NewJSONLWriter(w io.Writer) *JSONLWriter {
	encoder := json.NewEncoder(w)
	return &JSONLWriter{encoder: encoder}
}

// Write appends one result.
func (w *JSONLWriter) Write(result types.Result) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.encoder.Encode(result)
}

// WriteJSON exports results as an indented JSON array.
func WriteJSON(path string, results []types.Result) error {
	if err := ensureParentDir(path); err != nil {
		return err
	}
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create JSON: %w", err)
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(results)
}

// WriteCSV exports results as CSV.
func WriteCSV(path string, results []types.Result) error {
	if err := ensureParentDir(path); err != nil {
		return err
	}
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create CSV: %w", err)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	header := []string{
		"domain", "availability", "lifecycle", "confidence",
		"created_at", "expires_at", "updated_at",
		"expiring_soon", "expires_in_days",
		"registrar", "source", "http_status",
		"price_registration", "price_renewal", "price_currency",
		"statuses", "cache_hit", "error", "input", "checked_at",
	}
	if err := writer.Write(header); err != nil {
		return fmt.Errorf("write CSV header: %w", err)
	}

	for _, result := range results {
		expiresInDays := ""
		if result.ExpiresInDays != nil {
			expiresInDays = strconv.Itoa(*result.ExpiresInDays)
		}
		var priceReg, priceRen, priceCur string
		if result.Price != nil {
			priceReg = result.Price.RegistrationCost
			priceRen = result.Price.RenewalCost
			priceCur = result.Price.Currency
		}
		record := []string{
			result.Domain,
			string(result.Availability),
			result.Lifecycle,
			result.Confidence,
			result.CreatedAt,
			result.ExpiresAt,
			result.UpdatedAt,
			strconv.FormatBool(result.ExpiringSoon),
			expiresInDays,
			result.Registrar,
			result.Source,
			strconv.Itoa(result.HTTPStatus),
			priceReg,
			priceRen,
			priceCur,
			strings.Join(result.Statuses, ";"),
			strconv.FormatBool(result.CacheHit),
			result.Error,
			result.Input,
			result.CheckedAt.Format(time.RFC3339),
		}
		if err := writer.Write(record); err != nil {
			return fmt.Errorf("write CSV row: %w", err)
		}
	}
	writer.Flush()
	return writer.Error()
}

func ensureParentDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "." || dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	return nil
}
