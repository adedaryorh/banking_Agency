package providers

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
	"sync"
	"time"
)

type MockCollectionsProvider struct {
	mu          sync.Mutex
	accounts    map[string]*VirtualAccount
	byNumber    map[string]*VirtualAccount
	collections map[string]*Collection
	seq         uint64
}

type CollectionSimulator interface {
	SimulateDeposit(ctx context.Context, accountNumber string, amountMinor int64, senderName, narrative string) (string, error)
}

func NewMockCollectionsProvider() *MockCollectionsProvider {
	return &MockCollectionsProvider{
		accounts:    map[string]*VirtualAccount{},
		byNumber:    map[string]*VirtualAccount{},
		collections: map[string]*Collection{},
	}
}

func (m *MockCollectionsProvider) Info() ProviderInfo {
	return ProviderInfo{Name: "mock", Version: "dev", Sandbox: true}
}

func (m *MockCollectionsProvider) CreateVirtualAccount(ctx context.Context, req VirtualAccountRequest) (*VirtualAccount, error) {
	if strings.TrimSpace(req.Reference) == "" {
		return nil, fmt.Errorf("mock: virtual account requires a reference")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if a, ok := m.accounts[req.Reference]; ok {
		return a, nil
	}

	accountName := req.AccountName
	if strings.TrimSpace(accountName) == "" {
		accountName = req.Reference
	}
	va := &VirtualAccount{
		ProviderRef:   req.Reference,
		AccountNumber: mockAccountNumber(req.Reference),
		AccountName:   accountName,
		BankName:      "Mock VA Bank",
		BankCode:      "MCK",
		Permanent:     true,
	}
	m.accounts[req.Reference] = va
	m.byNumber[va.AccountNumber] = va
	return va, nil
}

// mockAccountNumber maps a customer reference to a stable 10-digit number in
// the "9xxxxxxxxx" range, reserved for the mock so it can never collide with a
// real NUBAN.
func mockAccountNumber(ref string) string {
	sum := sha256.Sum256([]byte(ref))
	n := binary.BigEndian.Uint64(sum[:8]) % 1000000000
	return fmt.Sprintf("9%09d", n)
}

func (m *MockCollectionsProvider) GetVirtualAccount(ctx context.Context, accountNumber string) (*VirtualAccount, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a, ok := m.byNumber[accountNumber]; ok {
		return a, nil
	}
	return nil, &Error{Code: ErrNotFound, Message: "virtual account not found"}
}

func (m *MockCollectionsProvider) GetCollection(ctx context.Context, providerRef string) (*Collection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.collections[providerRef]; ok {
		return c, nil
	}
	return nil, &Error{Code: ErrNotFound, Message: "collection not found"}
}

func (m *MockCollectionsProvider) SimulateDeposit(ctx context.Context, accountNumber string, amountMinor int64, senderName, narrative string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.byNumber[accountNumber]; !ok {
		return "", &Error{Code: ErrNotFound, Message: "no virtual account with that number"}
	}
	if amountMinor <= 0 {
		return "", fmt.Errorf("mock: simulated deposit amount must be positive")
	}
	m.seq++
	ref := fmt.Sprintf("mcksim-%d-%d", time.Now().UnixNano(), m.seq)
	m.collections[ref] = &Collection{
		ProviderRef:   ref,
		AccountNumber: accountNumber,
		AmountMinor:   amountMinor,
		Currency:      "NGN",
		Status:        CollectionSettled,
		SenderName:    senderName,
		Narrative:     narrative,
		OccurredAt:    time.Now(),
	}
	return ref, nil
}
