package handlers

import (
	"bytes"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"nabla/transfers-svc/internal/middleware"
)

func (h *TransferHandler) DownloadTransferReceipt(c *gin.Context) {
	userID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	transferID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		notFoundResponse(c, "transfer not found")
		return
	}

	t, err := h.svc.TransferByID(c.Request.Context(), userID, transferID)
	if err != nil {
		handleError(c, err)
		return
	}

	dto := toTransferDTO(t)
	dto.Recipient = h.attachRecipient(c.Request.Context(), userID, t.BeneficiaryID)
	sender := h.svc.ReceiptIdentityFor(c.Request.Context(), userID)

	pdf := buildTransferReceiptPDF(dto, sender.Name, sender.AccountNumber)
	filename := fmt.Sprintf("nablr-receipt-%s.pdf", safeFilenamePart(dto.Reference))
	c.Header("Content-Type", "application/pdf")
	c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
	c.Header("Cache-Control", "private, no-store")
	c.Data(http.StatusOK, "application/pdf", pdf)
}

func buildTransferReceiptPDF(t transferDTO, senderName, senderAccount string) []byte {
	recipientName, recipientBank, recipientAccount := "", "", ""
	if t.Recipient != nil {
		recipientName = t.Recipient.Name
		recipientBank = t.Recipient.Institution
		recipientAccount = t.Recipient.AccountNumber
		if t.Recipient.Username != "" && recipientAccount == "" {
			recipientAccount = t.Recipient.Username
		}
	}
	if senderName == "" {
		senderName = "Nablr Customer"
	}
	if senderAccount != "" {
		senderAccount = "Nablr | " + senderAccount
	}

	status := strings.Title(strings.ReplaceAll(t.Status, "_", " "))
	when := receiptWhen(t)
	fee := t.Fee.Formatted
	if fee == "" {
		fee = "NGN 0.00"
	}
	remark := strings.TrimSpace(t.Narrative)
	if remark == "" {
		remark = "Nil"
	}
	paymentType := "Outward Transfer"
	if t.Type == "internal" {
		paymentType = "Nablr Transfer"
	}
	transactionNo := t.Reference
	if t.SessionID != "" {
		transactionNo = t.SessionID
	}

	doc := newReceiptPDF()
	doc.text(54, 64, 30, "NABLR", fontBold, colorDark)
	doc.textRight(545, 66, 12, "TRANSACTION RECEIPT", fontBold, colorDark)
	doc.checkBadge(306, 112)
	doc.textCenter(306, 162, 18, status, fontRegular, colorGreenText)
	doc.textCenter(306, 198, 32, t.SendAmount.Formatted, fontRegular, colorDark)
	doc.textCenter(306, 226, 14, "on "+when, fontRegular, colorMuted)

	y := 284.0
	doc.row(&y, "Recipient's Details", recipientName, compactSubline(recipientBank, recipientAccount))
	doc.row(&y, "Sender's Details", senderName, senderAccount)
	doc.row(&y, "Transfer Fee", fee, "")
	doc.row(&y, "Payment Type", paymentType, "")
	doc.row(&y, "Transaction No.", transactionNo, "")
	doc.row(&y, "Remark", remark, "")

	doc.text(70, 742, 11, "www.nablr.com", fontRegular, colorDark)
	doc.textRight(530, 742, 11, "hello@nablr.com", fontRegular, colorDark)
	return doc.bytes()
}

func receiptWhen(t transferDTO) string {
	value := t.CompletedAt
	if value == "" {
		value = t.SubmittedAt
	}
	if value == "" {
		value = t.CreatedAt
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return value
	}
	return strings.ToUpper(parsed.Format("2 Jan 2006 at 03:04PM"))
}

func compactSubline(parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " | ")
}

func safeFilenamePart(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "transfer"
	}
	var b strings.Builder
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "transfer"
	}
	return b.String()
}

type simplePDF struct {
	objects []string
	content strings.Builder
}

type pdfColor struct{ r, g, b float64 }

const (
	fontRegular = "F1"
	fontBold    = "F2"
)

var (
	colorDark      = pdfColor{0.02, 0.03, 0.03}
	colorMuted     = pdfColor{0.45, 0.46, 0.56}
	colorLine      = pdfColor{0.86, 0.87, 0.89}
	colorGreen     = pdfColor{0.56, 0.91, 0.38}
	colorGreenText = pdfColor{0.32, 0.65, 0.25}
	colorWhite     = pdfColor{1, 1, 1}
)

func newReceiptPDF() *simplePDF {
	p := &simplePDF{}
	p.rect(36, 36, 540, 720, colorGreen)
	for x := 42.0; x < 570; x += 24 {
		for y := 46.0; y < 744; y += 24 {
			p.line(x, y, x+10, y+12, pdfColor{0.36, 0.76, 0.22}, 0.35)
			p.line(x+10, y+12, x+22, y, pdfColor{0.36, 0.76, 0.22}, 0.35)
		}
	}
	p.rect(56, 100, 500, 610, colorWhite)
	p.line(70, 260, 540, 260, colorLine, 0.6)
	p.line(70, 694, 540, 694, colorLine, 0.6)
	return p
}

func (p *simplePDF) checkBadge(cx, cy float64) {
	p.rect(cx-28, cy-28, 56, 56, colorGreen)
	p.line(cx-14, cy, cx-3, cy+12, colorWhite, 6)
	p.line(cx-3, cy+12, cx+18, cy-14, colorWhite, 6)
}

func (p *simplePDF) rect(x, y, w, h float64, c pdfColor) {
	p.content.WriteString(fmt.Sprintf("%.3f %.3f %.3f rg %.1f %.1f %.1f %.1f re f\n", c.r, c.g, c.b, x, 792-y-h, w, h))
}

func (p *simplePDF) line(x1, y1, x2, y2 float64, c pdfColor, width float64) {
	p.content.WriteString(fmt.Sprintf("%.3f %.3f %.3f RG %.1f w %.1f %.1f m %.1f %.1f l S\n", c.r, c.g, c.b, width, x1, 792-y1, x2, 792-y2))
}

func (p *simplePDF) text(x, y, size float64, text, font string, c pdfColor) {
	p.writeText(x, y, size, text, "", font, c)
}

func (p *simplePDF) textCenter(x, y, size float64, text, font string, c pdfColor) {
	p.writeText(x, y, size, text, "center", font, c)
}

func (p *simplePDF) textRight(x, y, size float64, text, font string, c pdfColor) {
	p.writeText(x, y, size, text, "right", font, c)
}

func (p *simplePDF) row(y *float64, label, value, subline string) {
	p.line(70, *y-20, 540, *y-20, colorLine, 0.45)
	p.text(70, *y, 12, label, fontRegular, colorMuted)
	p.textRight(530, *y, 12, value, fontRegular, colorDark)
	if subline != "" {
		*y += 18
		p.textRight(530, *y, 10, subline, fontRegular, colorMuted)
	}
	*y += 45
}

func (p *simplePDF) writeText(x, y, size float64, text, align, font string, c pdfColor) {
	text = pdfEscape(asPDFText(text))
	if align != "" {
		approxWidth := float64(len(text)) * size * 0.28
		if align == "center" {
			x -= approxWidth
		} else if align == "right" {
			x -= approxWidth * 2
		}
	}
	p.content.WriteString(fmt.Sprintf("%.3f %.3f %.3f rg BT /%s %.1f Tf %.1f %.1f Td (%s) Tj ET\n", c.r, c.g, c.b, font, size, x, 792-y, text))
}

func (p *simplePDF) bytes() []byte {
	content := p.content.String()
	p.objects = []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R /F2 5 0 R >> >> /Contents 6 0 R >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica-Bold >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
	}

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	for i, obj := range p.objects {
		offsets = append(offsets, buf.Len())
		buf.WriteString(strconv.Itoa(i+1) + " 0 obj\n")
		buf.WriteString(obj)
		buf.WriteString("\nendobj\n")
	}
	xref := buf.Len()
	buf.WriteString("xref\n0 " + strconv.Itoa(len(offsets)) + "\n")
	buf.WriteString("0000000000 65535 f \n")
	for i := 1; i < len(offsets); i++ {
		buf.WriteString(fmt.Sprintf("%010d 00000 n \n", offsets[i]))
	}
	buf.WriteString("trailer\n")
	buf.WriteString(fmt.Sprintf("<< /Size %d /Root 1 0 R >>\n", len(offsets)))
	buf.WriteString("startxref\n")
	buf.WriteString(strconv.Itoa(xref))
	buf.WriteString("\n%%EOF\n")
	return buf.Bytes()
}

func asPDFText(v string) string {
	replacer := strings.NewReplacer("₦", "NGN ", "•", "*", "’", "'", "→", "->")
	return replacer.Replace(v)
}

func pdfEscape(v string) string {
	v = strings.ReplaceAll(v, "\\", "\\\\")
	v = strings.ReplaceAll(v, "(", "\\(")
	v = strings.ReplaceAll(v, ")", "\\)")
	return v
}
