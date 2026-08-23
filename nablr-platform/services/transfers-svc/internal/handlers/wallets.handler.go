package handlers

import (
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"nabla/transfers-svc/internal/middleware"
	"nabla/transfers-svc/internal/models"
	"nabla/transfers-svc/internal/service"
)

type WalletHandler struct {
	wallets *service.WalletService
}

func NewWalletHandler(w *service.WalletService) *WalletHandler {
	return &WalletHandler{wallets: w}
}

type moneyView struct {
	Amount    int64  `json:"amount"`
	Currency  string `json:"currency"`
	Formatted string `json:"formatted"`
}

func money(amountMinor int64, currency string) moneyView {
	return moneyView{
		Amount:    amountMinor,
		Currency:  currency,
		Formatted: formatMinor(amountMinor, currency),
	}
}

// formatMinor renders a minor-unit amount as a fixed two-decimal string in the
// wallet currency, e.g. 123456 -> "1234.56".
func formatMinor(minor int64, currency string) string {
	neg := minor < 0
	if neg {
		minor = -minor
	}
	whole := minor / 100
	frac := minor % 100
	return fmt.Sprintf("%s%d.%02d %s", sign(neg), whole, frac, currency)
}

func sign(neg bool) string {
	if neg {
		return "-"
	}
	return ""
}

type walletView struct {
	ID        uuid.UUID `json:"id"`
	AccountID uuid.UUID `json:"account_id"`
	Currency  string    `json:"currency"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	IsDefault bool      `json:"is_default"`
	Status    string    `json:"status"`
	Balance   moneyView `json:"balance"`
	Available moneyView `json:"available"`
	Reserved  moneyView `json:"reserved"`
	Pending   moneyView `json:"pending"`
	CreatedAt string    `json:"created_at"`
}

func toWallet(w *models.Wallet) walletView {
	return walletView{
		ID:        w.ID,
		AccountID: w.AccountID,
		Currency:  w.Currency,
		Name:      w.Name,
		Type:      w.Type,
		IsDefault: w.IsDefault,
		Status:    w.Status,
		Balance:   money(w.BalanceMinor, w.Currency),
		Available: money(w.AvailableMinor, w.Currency),
		Reserved:  money(w.ReservedMinor, w.Currency),
		Pending:   money(w.PendingMinor, w.Currency),
		CreatedAt: w.CreatedAt.UTC().Format(time.RFC3339),
	}
}

type feedItemView struct {
	ID               uuid.UUID  `json:"id"`
	Direction        string     `json:"direction"`
	Amount           moneyView  `json:"amount"`
	Fee              moneyView  `json:"fee"`
	BalanceAfter     *moneyView `json:"balance_after,omitempty"`
	Type             string     `json:"type"`
	CounterpartyName string     `json:"counterparty_name,omitempty"`
	Description      string     `json:"description"`
	Status           string     `json:"status"`
	OccurredAt       string     `json:"occurred_at"`
	SourceType       string     `json:"source_type,omitempty"`
	SourceID         string     `json:"source_id,omitempty"`
	Reference        string     `json:"reference,omitempty"`
}

func toFeedItem(f models.FeedItem) feedItemView {
	out := feedItemView{
		ID:               f.ID,
		Direction:        f.Direction,
		Amount:           money(f.AmountMinor, f.Currency),
		Fee:              money(f.FeeMinor, f.Currency),
		Type:             f.TransactionType,
		CounterpartyName: f.CounterpartyName,
		Description:      f.Description,
		Status:           f.Status,
		OccurredAt:       f.OccurredAt.UTC().Format(time.RFC3339),
		SourceType:       f.SourceType,
		Reference:        f.Reference,
	}
	if f.BalanceAfterMinor != nil {
		b := money(*f.BalanceAfterMinor, f.Currency)
		out.BalanceAfter = &b
	}
	if f.SourceID != uuid.Nil {
		out.SourceID = f.SourceID.String()
	}
	return out
}

// CreateWallet handles POST /api/v1/wallets. Provisions a wallet and its
// backing ledger account for the customer, in the currency they open it in.
func (h *WalletHandler) CreateWallet(c *gin.Context) {
	customerID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	var in struct {
		Currency string `json:"currency"`
		Name     string `json:"name"`
	}
	if c.ShouldBindJSON(&in) != nil {
		badRequest(c, "invalid request body")
		return
	}
	wallet, err := h.wallets.OpenWallet(c.Request.Context(), customerID, in.Currency, in.Name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "We could not open your wallet just now."})
		return
	}
	c.JSON(http.StatusCreated, toWallet(wallet))
}

// ListWallets handles GET /api/v1/wallets.
func (h *WalletHandler) ListWallets(c *gin.Context) {
	customerID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	wallets, err := h.wallets.WalletsForCustomer(c.Request.Context(), customerID)
	if err != nil {
		internalError(c, "We could not load your wallets just now.")
		return
	}
	out := make([]walletView, 0, len(wallets))
	for i := range wallets {
		out = append(out, toWallet(&wallets[i]))
	}
	c.JSON(http.StatusOK, out)
}

// GetWallet handles GET /api/v1/wallets/:id.
func (h *WalletHandler) GetWallet(c *gin.Context) {
	customerID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	walletID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		notFoundResponse(c, "wallet not found")
		return
	}
	wallet, err := h.wallets.WalletByID(c.Request.Context(), walletID, customerID)
	if err != nil {
		if errors.Is(err, models.ErrWalletNotFound) {
			notFoundResponse(c, "wallet not found")
			return
		}
		internalError(c, "We could not load your wallet just now.")
		return
	}
	c.JSON(http.StatusOK, toWallet(wallet))
}

// ListTransactions handles GET /api/v1/wallets/:id/transactions with cursor
// pagination: ?cursor_time=...&cursor_id=...&limit=...
func (h *WalletHandler) ListTransactions(c *gin.Context) {
	customerID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	walletID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		notFoundResponse(c, "wallet not found")
		return
	}

	var before time.Time
	if v := c.Query("cursor_time"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			badRequest(c, "cursor_time must be an RFC3339 timestamp")
			return
		}
		before = t
	}
	var beforeID uuid.UUID
	if v := c.Query("cursor_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			badRequest(c, "cursor_id is not a valid id")
			return
		}
		beforeID = id
	}
	limit := 25
	if v := c.Query("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			badRequest(c, "limit must be a positive integer")
			return
		}
		limit = n
	}

	items, err := h.wallets.Feed(c.Request.Context(), walletID, customerID, before, beforeID, limit+1)
	if err != nil {
		if errors.Is(err, models.ErrWalletNotFound) {
			notFoundResponse(c, "wallet not found")
			return
		}
		internalError(c, "We could not load your activity just now.")
		return
	}

	hasMore := false
	if len(items) > limit {
		items = items[:limit]
		hasMore = true
	}
	var next *gin.H
	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		next = &gin.H{"cursor_time": last.OccurredAt.UTC().Format(time.RFC3339), "cursor_id": last.ID.String()}
	}

	out := make([]feedItemView, 0, len(items))
	for _, f := range items {
		out = append(out, toFeedItem(f))
	}
	c.JSON(http.StatusOK, gin.H{"items": out, "has_more": hasMore, "next": next})
}

// Insights handles GET /api/v1/wallets/:id/insights.
func (h *WalletHandler) Insights(c *gin.Context) {
	customerID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	walletID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		notFoundResponse(c, "wallet not found")
		return
	}
	in, err := h.wallets.Insights(c.Request.Context(), walletID, customerID)
	if err != nil {
		if errors.Is(err, models.ErrWalletNotFound) {
			notFoundResponse(c, "wallet not found")
			return
		}
		internalError(c, "We could not load your month just now.")
		return
	}

	byType := make([]map[string]any, 0, len(in.ByType))
	for _, t := range in.ByType {
		byType = append(byType, map[string]any{
			"type": t.Type, "amount": money(t.AmountMinor, in.Currency),
			"share_pct": t.SharePct, "count": t.Count,
		})
	}
	commitments := make([]map[string]any, 0, len(in.Commitments))
	for _, c := range in.Commitments {
		commitments = append(commitments, map[string]any{
			"id": c.ID.String(), "name": c.Name,
			"amount": money(c.AmountMinor, in.Currency),
			"due_at": c.DueAt.UTC().Format(time.RFC3339), "frequency": c.Frequency,
		})
	}

	out := map[string]any{
		"currency":       in.Currency,
		"available":      money(in.AvailableMinor, in.Currency),
		"on_hold":        money(in.OnHoldMinor, in.Currency),
		"month_in":       money(in.MonthIn, in.Currency),
		"month_out":      money(in.MonthOut, in.Currency),
		"month_net":      money(in.MonthIn-in.MonthOut, in.Currency),
		"last_month_out": money(in.LastMonthOut, in.Currency),
		"days_elapsed":   in.DaysElapsed,
		"days_in_month":  in.DaysInMonth,
		"daily_out":      money(in.DailyOut, in.Currency),
		"projected_out":  money(in.ProjectedOut, in.Currency),
		"giving":         money(in.GivingMinor, in.Currency),
		"committed":      money(in.CommittedMinor, in.Currency),
		"safe_to_spend":  money(in.SafeToSpendMinor, in.Currency),
		"by_type":        byType,
		"commitments":    commitments,
	}
	if in.OutChangePct != nil {
		out["out_change_pct"] = *in.OutChangePct
	}
	if in.Biggest != nil {
		out["biggest"] = map[string]any{
			"description": in.Biggest.Description,
			"amount":      money(in.Biggest.AmountMinor, in.Currency),
			"occurred_at": in.Biggest.OccurredAt.UTC().Format(time.RFC3339),
		}
	}
	c.JSON(http.StatusOK, out)
}

// Statement handles GET /api/v1/wallets/:id/statement.csv?from=YYYY-MM-DD&to=YYYY-MM-DD.
// Amounts are plain decimal strings in the wallet currency, oldest first.
// Defaults to the last 30 days; the range is capped at one year.
func (h *WalletHandler) Statement(c *gin.Context) {
	customerID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	walletID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		notFoundResponse(c, "wallet not found")
		return
	}

	const day = 24 * time.Hour
	to := time.Now().UTC().Truncate(day).Add(day)
	if v := c.Query("to"); v != "" {
		d, err := time.Parse("2006-01-02", v)
		if err != nil {
			badRequest(c, "to must use the YYYY-MM-DD format")
			return
		}
		to = d.Add(day)
	}
	from := to.Add(-31 * day)
	if v := c.Query("from"); v != "" {
		d, err := time.Parse("2006-01-02", v)
		if err != nil {
			badRequest(c, "from must use the YYYY-MM-DD format")
			return
		}
		from = d
	}
	if !from.Before(to) || to.Sub(from) > 366*day {
		badRequest(c, "the statement range must be positive and at most one year")
		return
	}

	items, err := h.wallets.Statement(c.Request.Context(), walletID, customerID, from, to)
	if err != nil {
		if errors.Is(err, models.ErrWalletNotFound) {
			notFoundResponse(c, "wallet not found")
			return
		}
		internalError(c, "We could not build your statement just now.")
		return
	}

	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition",
		`attachment; filename="statement-`+from.Format("2006-01-02")+`-to-`+
			to.Add(-day).Format("2006-01-02")+`.csv"`)

	cw := csv.NewWriter(c.Writer)
	_ = cw.Write([]string{"date", "type", "direction", "description",
		"counterparty", "category", "amount", "fee", "balance_after",
		"currency", "pending"})
	for _, f := range items {
		balanceAfter := ""
		if f.BalanceAfterMinor != nil {
			balanceAfter = formatMinor(*f.BalanceAfterMinor, f.Currency)
		}
		_ = cw.Write([]string{
			f.OccurredAt.UTC().Format(time.RFC3339),
			f.TransactionType, f.Direction, f.Description,
			f.CounterpartyName, "",
			formatMinor(f.AmountMinor, f.Currency),
			formatMinor(f.FeeMinor, f.Currency),
			balanceAfter,
			f.Currency, "false",
		})
	}
	cw.Flush()
}

func internalError(c *gin.Context, msg string) {
	c.JSON(http.StatusInternalServerError, gin.H{"error": msg})
}
