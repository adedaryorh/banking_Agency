package service

import (
	"testing"

	"github.com/google/uuid"

	db "nabla/transfers-svc/db/sqlc"
)

// A card authorisation and a transfer reserve compete for the SAME money.
//
// This is the test that would have caught the split. The card holds most of the
// balance; the transfer engine's reserve must then fail for want of funds, and
// it can only fail if it is looking at the balance the card just moved.
func TestOneBalance_CardHoldStarvesTheTransferEngine(t *testing.T) {
	h := newCardHarness(t, 10_000_00)
	card := h.issueVirtual()
	spendable := h.available()

	// Take all but ₦1 of it on the card.
	d, err := h.cards.Authorize(h.ctx, AuthRequest{
		ProviderAuthID: "one_balance", CardID: card.ID,
		AmountMinor: spendable - 100, Currency: "NGN",
		MerchantName: "Shoprite", MerchantMCC: "5411", EntryMode: "chip",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Approved {
		t.Fatalf("the card was declined: %s", d.DeclineReason)
	}

	q := db.New(h.pool)

	// ₦2 through the transfer engine must now be refused: only ₦1 is left.
	n, err := q.ReserveWallet(h.ctx, db.ReserveWalletParams{
		WalletID: h.walletID, AmountMinor: 200,
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("the transfer engine reserved money the card had already taken — " +
			"the two rails are looking at different balances")
	}

	// ₦1 exactly is still there, and is the last of it.
	n, err = q.ReserveWallet(h.ctx, db.ReserveWalletParams{
		WalletID: h.walletID, AmountMinor: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("the transfer engine could not reserve the ₦1 the card left behind")
	}
	if h.available() != 0 {
		t.Errorf("available = %d, want 0", h.available())
	}
}

// And the reverse: money reserved by the transfer engine is invisible to a
// card.
func TestOneBalance_TransferReserveDeclinesTheCard(t *testing.T) {
	h := newCardHarness(t, 10_000_00)
	card := h.issueVirtual()
	spendable := h.available()

	q := db.New(h.pool)
	n, err := q.ReserveWallet(h.ctx, db.ReserveWalletParams{
		WalletID: h.walletID, AmountMinor: spendable,
	})
	if err != nil || n != 1 {
		t.Fatalf("reserve: n=%d err=%v", n, err)
	}

	d, err := h.cards.Authorize(h.ctx, AuthRequest{
		ProviderAuthID: "one_balance_2", CardID: card.ID,
		AmountMinor: 100, Currency: "NGN",
		MerchantName: "Shoprite", MerchantMCC: "5411", EntryMode: "chip",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Approved {
		t.Fatal("the card spent money the transfer engine had already reserved")
	}
	if d.DeclineReason != "insufficient_funds" {
		t.Errorf("decline reason = %q, want insufficient_funds", d.DeclineReason)
	}
}

// available_minor is derived, so it cannot be written and cannot drift.
func TestOneBalance_AvailableIsGenerated(t *testing.T) {
	h := newCardHarness(t, 5_000_00)

	if _, err := h.pool.Exec(h.ctx,
		`UPDATE ledger_accounts SET available_minor = 999 WHERE id = $1`, h.accountID); err == nil {
		t.Fatal("available_minor was writable; it must be GENERATED so it cannot disagree " +
			"with the balance and reservation it is derived from")
	}

	var balance, reserved, available int64
	if err := h.pool.QueryRow(h.ctx, `
		SELECT balance_minor, reserved_minor, available_minor
		  FROM ledger_accounts WHERE id = $1`, h.accountID).
		Scan(&balance, &reserved, &available); err != nil {
		t.Fatal(err)
	}
	if available != balance-reserved {
		t.Errorf("available %d != balance %d - reserved %d", available, balance, reserved)
	}
}

// A customer cannot be driven overdrawn, and the refusal comes from the
// database rather than from a check in Go that a concurrent writer could slip
// past.
func TestOneBalance_NoOverdraftOnCustomerMoney(t *testing.T) {
	h := newCardHarness(t, 1_000_00)

	if _, err := h.pool.Exec(h.ctx,
		`UPDATE ledger_accounts SET balance_minor = balance_minor - 500_00 WHERE id = $1`,
		h.accountID); err != nil {
		t.Fatalf("a debit within the balance should be allowed: %v", err)
	}
	if _, err := h.pool.Exec(h.ctx,
		`UPDATE ledger_accounts SET balance_minor = balance_minor - 5_000_00 WHERE id = $1`,
		h.accountID); err == nil {
		t.Fatal("a customer account was driven negative; ledger_accounts_no_overdraft did not hold")
	}

	// Platform positions may legitimately sit either side of zero.
	var platform uuid.UUID
	if err := h.pool.QueryRow(h.ctx, `
		INSERT INTO ledger_accounts
			(account_type, currency, owner_type, name, code, allows_negative, status)
		VALUES ('suspense', 'NGN', 'platform', 'suspense NGN', 'suspense:NGN:test', true, 'active')
		RETURNING id`).Scan(&platform); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(h.ctx,
		`UPDATE ledger_accounts SET balance_minor = -1_000_00 WHERE id = $1`, platform); err != nil {
		t.Errorf("a platform position could not go negative: %v", err)
	}
}

// The wallets columns are a mirror. They are no longer the truth, but a stale
// mirror would make every balance the app shows wrong, so it is checked.
func TestOneBalance_WalletMirrorFollowsTheLedger(t *testing.T) {
	h := newCardHarness(t, 4_000_00)
	card := h.issueVirtual()

	if _, err := h.cards.Authorize(h.ctx, AuthRequest{
		ProviderAuthID: "mirror", CardID: card.ID,
		AmountMinor: 1_500_00, Currency: "NGN",
		MerchantName: "Shoprite", MerchantMCC: "5411", EntryMode: "chip",
	}); err != nil {
		t.Fatal(err)
	}

	var wAvailable, wReserved, aAvailable, aReserved int64
	if err := h.pool.QueryRow(h.ctx, `
		SELECT w.available_minor, w.reserved_minor, la.available_minor, la.reserved_minor
		  FROM wallets w JOIN ledger_accounts la ON la.id = w.ledger_account_id
		 WHERE w.id = $1`, h.walletID).
		Scan(&wAvailable, &wReserved, &aAvailable, &aReserved); err != nil {
		t.Fatal(err)
	}
	if wAvailable != aAvailable || wReserved != aReserved {
		t.Errorf("the wallet mirror has drifted: wallet(%d/%d) vs account(%d/%d)",
			wAvailable, wReserved, aAvailable, aReserved)
	}
}

// A wallet created the ordinary way gets its backing account without the caller
// asking, because a wallet with nowhere to put its balance is not a wallet.
func TestOneBalance_NewWalletOpensItsAccount(t *testing.T) {
	h := newCardHarness(t, 0)
	q := db.New(h.pool)

	user := uuid.New()
	w, err := q.CreateWallet(h.ctx, db.CreateWalletParams{UserID: user, Currency: "NGN"})
	if err != nil {
		t.Fatal(err)
	}

	var accountID uuid.UUID
	if err := h.pool.QueryRow(h.ctx,
		`SELECT ledger_account_id FROM wallets WHERE id = $1`, w.ID).Scan(&accountID); err != nil {
		t.Fatalf("the new wallet has no backing ledger account: %v", err)
	}

	// And it is spendable through both rails from the moment it is funded.
	if err := q.CreditWallet(h.ctx, db.CreditWalletParams{WalletID: w.ID, AmountMinor: 2_000_00}); err != nil {
		t.Fatal(err)
	}
	var available int64
	if err := h.pool.QueryRow(h.ctx,
		`SELECT available_minor FROM ledger_accounts WHERE id = $1`, accountID).Scan(&available); err != nil {
		t.Fatal(err)
	}
	if available != 2_000_00 {
		t.Errorf("available = %d, want 200000", available)
	}
}
