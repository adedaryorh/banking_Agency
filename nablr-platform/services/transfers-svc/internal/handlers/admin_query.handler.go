package handlers

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AdminQueryHandler runs operator-supplied SQL directly against this
// service's own database. It exists so an operator never needs a direct
// network path to the live database — everything goes through this one
// audited, rate-bounded chokepoint instead. It is gated upstream by
// middleware.AdminAuth; this handler itself does no authentication.
type AdminQueryHandler struct {
	pool *pgxpool.Pool
}

func NewAdminQueryHandler(pool *pgxpool.Pool) *AdminQueryHandler {
	return &AdminQueryHandler{pool: pool}
}

// adminQueryTimeout bounds how long a single admin query may run. A raw SQL
// endpoint with no timeout is one long-running query away from a self-inflicted
// outage; 30s is generous for an ops query and short enough to bail out of an
// accidental full-table scan or a lock wait.
const adminQueryTimeout = 30 * time.Second

// adminQueryRowCap bounds how many rows are serialised into the response. It
// protects the caller (a very wide SELECT can otherwise produce a
// multi-hundred-MB JSON body) — it does not limit what the query itself is
// allowed to touch or how many rows it may write.
const adminQueryRowCap = 1000

type adminQueryRequest struct {
	SQL  string `json:"sql"`
	Args []any  `json:"args"`
}

// RawQuery — POST /admin/db/query
//
// Runs exactly the SQL given, with no parsing, allow-listing or read/write
// restriction: this is intentionally full raw SQL, by explicit operator
// choice. Every call is logged before it runs — actor, remote IP, and the
// SQL text — so a destructive statement is never silent, even one that fails.
func (h *AdminQueryHandler) RawQuery(c *gin.Context) {
	actor := strings.TrimSpace(c.GetHeader("X-Admin-Actor"))

	var req adminQueryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "request body is not valid JSON"})
		return
	}
	sql := strings.TrimSpace(req.SQL)
	if sql == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "sql is required"})
		return
	}

	log.Printf("admin db query attempt actor=%q remote_ip=%s sql=%q", actor, c.ClientIP(), sql)

	ctx, cancel := context.WithTimeout(c.Request.Context(), adminQueryTimeout)
	defer cancel()

	start := time.Now()
	rows, err := h.pool.Query(ctx, sql, req.Args...)
	if err != nil {
		log.Printf("admin db query failed actor=%q sql=%q error=%v", actor, sql, err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	fields := rows.FieldDescriptions()
	columns := make([]string, len(fields))
	for i, f := range fields {
		columns[i] = string(f.Name)
	}

	results := make([]map[string]any, 0)
	truncated := false
	for rows.Next() {
		if len(results) >= adminQueryRowCap {
			truncated = true
			break
		}
		values, err := rows.Values()
		if err != nil {
			log.Printf("admin db query row scan failed actor=%q sql=%q error=%v", actor, sql, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		row := make(map[string]any, len(columns))
		for i, col := range columns {
			if i < len(values) {
				row[col] = values[i]
			}
		}
		results = append(results, row)
	}
	queryErr := rows.Err()
	tag := rows.CommandTag()
	rows.Close()
	duration := time.Since(start)

	if queryErr != nil && !errors.Is(queryErr, context.Canceled) {
		log.Printf("admin db query failed mid-read actor=%q sql=%q error=%v", actor, sql, queryErr)
		c.JSON(http.StatusInternalServerError, gin.H{"error": queryErr.Error()})
		return
	}

	log.Printf("admin db query ok actor=%q sql=%q row_count=%d rows_affected=%d truncated=%t duration_ms=%d",
		actor, sql, len(results), tag.RowsAffected(), truncated, duration.Milliseconds())

	c.JSON(http.StatusOK, gin.H{
		"columns":       columns,
		"rows":          results,
		"row_count":     len(results),
		"rows_affected": tag.RowsAffected(),
		"truncated":     truncated,
		"duration_ms":   duration.Milliseconds(),
	})
}
