package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

// RedeemTypePromo identifies a promo reward in the unified user redemption API.
// It is not a persisted redeem-code type.
const RedeemTypePromo = "promo"

type UserRedemptionHistoryRepository interface {
	ListByUser(context.Context, int64, pagination.PaginationParams) ([]RedeemCode, *pagination.PaginationResult, error)
}

func PromoUsageAsRedemption(usage *PromoCodeUsage) *RedeemCode {
	result := &RedeemCode{
		ID: usage.ID, Type: RedeemTypePromo, Value: usage.BonusAmount,
		Status: StatusUsed, UsedBy: &usage.UserID, UsedAt: &usage.UsedAt,
		CreatedAt: usage.UsedAt,
	}
	if usage.PromoCode != nil {
		result.Code = usage.PromoCode.Code
	}
	return result
}

type RedeemCode struct {
	ID        int64
	Code      string
	Type      string
	Value     float64
	Status    string
	UsedBy    *int64
	UsedAt    *time.Time
	Notes     string
	CreatedAt time.Time
	ExpiresAt *time.Time

	GroupID      *int64
	ValidityDays int

	User  *User
	Group *Group
}

func (r *RedeemCode) IsUsed() bool {
	return r.Status == StatusUsed
}

func (r *RedeemCode) IsExpired() bool {
	return r.IsExpiredAt(time.Now())
}

func (r *RedeemCode) IsExpiredAt(now time.Time) bool {
	if r == nil {
		return false
	}
	if r.Status == StatusExpired {
		return true
	}
	return r.Status == StatusUnused && r.ExpiresAt != nil && !r.ExpiresAt.After(now)
}

func (r *RedeemCode) CanUse() bool {
	return r.Status == StatusUnused && !r.IsExpired()
}

func GenerateRedeemCode() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
