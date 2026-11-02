//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type redemptionPromoRepo struct {
	PromoCodeRepository
	code    PromoCode
	usages  map[int64]*PromoCodeUsage
	lookups int
	err     error
}

func (r *redemptionPromoRepo) GetByCodeForUpdate(_ context.Context, code string) (*PromoCode, error) {
	r.lookups++
	if r.err != nil {
		return nil, r.err
	}
	if code != r.code.Code {
		return nil, ErrPromoCodeNotFound
	}
	c := r.code
	return &c, nil
}
func (r *redemptionPromoRepo) GetUsageByPromoCodeAndUser(_ context.Context, _ int64, userID int64) (*PromoCodeUsage, error) {
	return r.usages[userID], nil
}
func (r *redemptionPromoRepo) CreateUsage(_ context.Context, usage *PromoCodeUsage) error {
	usage.ID = int64(len(r.usages) + 1)
	r.usages[usage.UserID] = usage
	return nil
}
func (r *redemptionPromoRepo) IncrementUsedCount(context.Context, int64) error {
	r.code.UsedCount++
	return nil
}

func newPromoRedemptionTestService(t *testing.T) (*RedeemService, *redemptionPromoRepo, *redeemRateLimitCacheStub, map[int64]float64) {
	t.Helper()
	client := newPaymentConfigServiceTestClient(t)
	repo := &redemptionPromoRepo{
		code:   PromoCode{ID: 11, Code: "PROMO", BonusAmount: 5, Status: PromoCodeStatusActive},
		usages: make(map[int64]*PromoCodeUsage),
	}
	balances := make(map[int64]float64)
	userRepo := &mockUserRepo{getByIDUser: &User{ID: 1}}
	userRepo.updateBalanceFn = func(_ context.Context, userID int64, amount float64) error {
		balances[userID] += amount
		return nil
	}
	promo := NewPromoService(repo, userRepo, nil, client, nil)
	settings := NewSettingService(&settingRepoStub{values: map[string]string{SettingKeyPromoCodeEnabled: "true"}}, nil)
	cache := &redeemRateLimitCacheStub{}
	redeems := &paymentOrderLifecycleRedeemRepo{codesByCode: make(map[string]*RedeemCode)}
	svc := ProvideRedeemService(redeems, userRepo, nil, cache, nil, client, nil, nil, promo, settings, nil)
	return svc, repo, cache, balances
}

func TestRedeemPromoSharedClaimsAndLimits(t *testing.T) {
	ctx := context.Background()
	svc, repo, cache, balances := newPromoRedemptionTestService(t)
	repo.code.MaxUses = 3

	// Registration and manual redemption share exactly the same usage records.
	require.NoError(t, svc.promoService.ApplyPromoCode(ctx, 1, "PROMO"))
	_, err := svc.Redeem(ctx, 1, "PROMO")
	require.ErrorIs(t, err, ErrPromoCodeAlreadyUsed)
	require.Equal(t, float64(5), balances[1])

	result, err := svc.Redeem(ctx, 2, " PROMO ")
	require.NoError(t, err)
	require.Equal(t, RedeemTypePromo, result.Type)
	require.Equal(t, float64(5), result.Value)
	require.Equal(t, "PROMO", result.Code)
	require.Equal(t, int64(2), *result.UsedBy)
	require.NotNil(t, result.UsedAt)
	require.Equal(t, float64(5), balances[2])
	_, err = svc.Redeem(ctx, 2, "PROMO")
	require.ErrorIs(t, err, ErrPromoCodeAlreadyUsed)
	_, err = svc.Redeem(ctx, 3, "PROMO")
	require.NoError(t, err)
	_, err = svc.Redeem(ctx, 4, "PROMO")
	require.ErrorIs(t, err, ErrPromoCodeMaxUsed)
	require.Zero(t, balances[4])
	require.Equal(t, 3, repo.code.UsedCount)
	require.Equal(t, 3, cache.incrementCalls)
	// Shared promo claims are serialized by the promo row lock; the per-code
	// redeem lock would reject concurrent claimants instead of queueing them.
	require.Zero(t, cache.acquireCalls)
}

func TestRedeemCodeTakesPrecedenceOverPromo(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	for _, tt := range []struct {
		name      string
		status    string
		expiresAt *time.Time
		kind      string
		wantErr   error
	}{
		{"unused", StatusUnused, nil, RedeemTypeBalance, nil},
		{"used", StatusUsed, nil, RedeemTypeBalance, ErrRedeemCodeUsed},
		{"expired", StatusUnused, &past, RedeemTypeBalance, ErrRedeemCodeExpired},
		{"invitation", StatusUnused, nil, RedeemTypeInvitation, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc, promoRepo, _, _ := newPromoRedemptionTestService(t)
			repo := svc.redeemRepo.(*paymentOrderLifecycleRedeemRepo)
			repo.codesByCode["PROMO"] = &RedeemCode{ID: 1, Code: "PROMO", Type: tt.kind, Value: 10, Status: tt.status, ExpiresAt: tt.expiresAt}
			result, err := svc.Redeem(context.Background(), 1, "PROMO")
			if tt.kind == RedeemTypeInvitation {
				require.Error(t, err)
			} else if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
				require.Equal(t, RedeemTypeBalance, result.Type)
			}
			require.Zero(t, promoRepo.lookups)
			require.Empty(t, promoRepo.usages)
		})
	}
}

func TestRedeemPromoFailuresAndRateLimit(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	for _, tt := range []struct {
		name      string
		configure func(*RedeemService, *redemptionPromoRepo, *redeemRateLimitCacheStub)
		wantErr   error
		wantCount int
	}{
		{"missing", func(_ *RedeemService, r *redemptionPromoRepo, _ *redeemRateLimitCacheStub) { r.code.Code = "OTHER" }, ErrRedeemCodeNotFound, 1},
		{"expired", func(_ *RedeemService, r *redemptionPromoRepo, _ *redeemRateLimitCacheStub) { r.code.ExpiresAt = &past }, ErrPromoCodeExpired, 1},
		{"disabled code", func(_ *RedeemService, r *redemptionPromoRepo, _ *redeemRateLimitCacheStub) {
			r.code.Status = PromoCodeStatusDisabled
		}, ErrPromoCodeDisabled, 1},
		{"disabled feature", func(s *RedeemService, _ *redemptionPromoRepo, _ *redeemRateLimitCacheStub) {
			s.settingService = NewSettingService(&settingRepoStub{values: map[string]string{SettingKeyPromoCodeEnabled: "false"}}, nil)
		}, ErrRedeemCodeNotFound, 1},
		{"settings read failure", func(s *RedeemService, _ *redemptionPromoRepo, _ *redeemRateLimitCacheStub) {
			s.settingService = NewSettingService(&settingRepoStub{err: errors.New("settings unavailable")}, nil)
		}, ErrRedeemCodeNotFound, 1},
		{"rate limited", func(_ *RedeemService, _ *redemptionPromoRepo, c *redeemRateLimitCacheStub) {
			c.count = redeemMaxFailedAttempts
		}, ErrRedeemRateLimited, 0},
		{"storage failure", func(_ *RedeemService, r *redemptionPromoRepo, _ *redeemRateLimitCacheStub) {
			r.err = errors.New("storage unavailable")
		}, nil, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc, repo, cache, balances := newPromoRedemptionTestService(t)
			tt.configure(svc, repo, cache)
			_, err := svc.Redeem(context.Background(), 1, "PROMO")
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.EqualError(t, err, "storage unavailable")
			}
			require.Equal(t, tt.wantCount, cache.incrementCalls)
			require.Empty(t, balances)
			if tt.name == "rate limited" || tt.name == "disabled feature" || tt.name == "settings read failure" {
				require.Zero(t, repo.lookups)
			}
		})
	}
}

func TestSuccessfulPromoDoesNotConsumeFailureAttempt(t *testing.T) {
	svc, _, cache, _ := newPromoRedemptionTestService(t)
	cache.count = redeemMaxFailedAttempts - 1
	_, err := svc.Redeem(context.Background(), 1, "PROMO")
	require.NoError(t, err)
	require.Zero(t, cache.incrementCalls)
}

func TestTrustedFulfillmentNeverFallsBackToPromo(t *testing.T) {
	svc, repo, _, _ := newPromoRedemptionTestService(t)
	_, err := svc.RedeemForAdminFulfillment(context.Background(), 1, "PROMO")
	require.ErrorIs(t, err, ErrRedeemCodeNotFound)
	_, err = svc.redeemForPaymentFulfillment(context.Background(), 1, "PROMO")
	require.ErrorIs(t, err, ErrRedeemCodeNotFound)
	require.Zero(t, repo.lookups)
}
