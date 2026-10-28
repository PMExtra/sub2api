package repository

import (
	"context"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUserRedemptionHistoryPaginationAndIsolation(t *testing.T) {
	client := newSecuritySecretTestClient(t)
	ctx := context.Background()
	user := client.User.Create().SetEmail("history@example.com").SetPasswordHash("hash").SaveX(ctx)
	other := client.User.Create().SetEmail("other@example.com").SetPasswordHash("hash").SaveX(ctx)
	group := client.Group.Create().SetName("Subscription").SaveX(ctx)
	promo := client.PromoCode.Create().SetCode("PROMO").SetBonusAmount(50).SaveX(ctx)
	base := time.Now().UTC().Truncate(time.Second)
	old := client.RedeemCode.Create().SetCode("REDEEM-OLD").SetUsedBy(user.ID).SetStatus(service.StatusUsed).SetUsedAt(base).SaveX(ctx)
	// The same ID can occur in both sources. The claimed amount is a snapshot,
	// independent of later edits to the promo's reward.
	usage := client.PromoCodeUsage.Create().SetPromoCodeID(promo.ID).SetUserID(user.ID).SetBonusAmount(5).SetUsedAt(base.Add(time.Minute)).SaveX(ctx)
	require.Equal(t, old.ID, usage.ID)
	client.RedeemCode.Create().SetCode("REDEEM-TIE").SetUsedBy(user.ID).SetStatus(service.StatusUsed).SetUsedAt(base.Add(time.Minute)).SaveX(ctx)
	client.RedeemCode.Create().SetCode("SUBSCRIPTION").SetType(service.RedeemTypeSubscription).SetGroupID(group.ID).SetUsedBy(user.ID).SetStatus(service.StatusUsed).SetUsedAt(base.Add(2 * time.Minute)).SaveX(ctx)
	client.PromoCodeUsage.Create().SetPromoCodeID(promo.ID).SetUserID(other.ID).SetBonusAmount(9).SetUsedAt(base.Add(time.Hour)).SaveX(ctx)
	client.RedeemCode.Create().SetCode("UNUSED").SaveX(ctx)

	repo := NewUserRedemptionHistoryRepository(client)
	var all []service.RedeemCode
	for page := 1; page <= 3; page++ {
		items, result, err := repo.ListByUser(ctx, user.ID, pagination.PaginationParams{Page: page, PageSize: 2})
		require.NoError(t, err)
		require.Equal(t, int64(4), result.Total)
		if page == 3 {
			require.Empty(t, items)
		}
		for _, item := range items {
			require.Equal(t, user.ID, *item.UsedBy)
		}
		all = append(all, items...)
	}
	require.Len(t, all, 4)
	require.Equal(t, "SUBSCRIPTION", all[0].Code)
	require.NotNil(t, all[0].Group)
	require.Equal(t, "Subscription", all[0].Group.Name)
	require.Equal(t, "PROMO", all[1].Code)
	require.Equal(t, service.RedeemTypePromo, all[1].Type)
	require.Equal(t, float64(5), all[1].Value)
	require.Equal(t, "REDEEM-TIE", all[2].Code)
	require.Equal(t, "REDEEM-OLD", all[3].Code)

	items, result, err := repo.ListByUser(ctx, other.ID, pagination.PaginationParams{Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.Equal(t, int64(1), result.Total)
	require.Len(t, items, 1)
	require.Equal(t, float64(9), items[0].Value)
	items, result, err = repo.ListByUser(ctx, other.ID+100, pagination.PaginationParams{Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.NotNil(t, items)
	require.Empty(t, items)
	require.Zero(t, result.Total)
}

func TestPromoUsageAndHistoryUseTransactionContext(t *testing.T) {
	client := newSecuritySecretTestClient(t)
	ctx := context.Background()
	user := client.User.Create().SetEmail("transaction@example.com").SetPasswordHash("hash").SaveX(ctx)
	promo := client.PromoCode.Create().SetCode("TRANSACTION").SaveX(ctx)
	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	defer tx.Rollback()
	txCtx := dbent.NewTxContext(ctx, tx)
	repo := NewPromoCodeRepository(client)
	usage := &service.PromoCodeUsage{PromoCodeID: promo.ID, UserID: user.ID, BonusAmount: 5, UsedAt: time.Now()}
	require.NoError(t, repo.CreateUsage(txCtx, usage))
	got, err := repo.GetUsageByPromoCodeAndUser(txCtx, promo.ID, user.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, usage.ID, got.ID)

	_, err = tx.Client().PromoCodeUsage.Create().SetPromoCodeID(promo.ID).SetUserID(user.ID).SetBonusAmount(5).Save(txCtx)
	require.True(t, dbent.IsConstraintError(err), "the database must prevent duplicate claims")
	items, result, err := NewUserRedemptionHistoryRepository(client).ListByUser(txCtx, user.ID, pagination.PaginationParams{Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.Equal(t, int64(1), result.Total)
	require.Len(t, items, 1)
	require.Equal(t, "TRANSACTION", items[0].Code)
	require.NoError(t, tx.Rollback())
	got, err = repo.GetUsageByPromoCodeAndUser(ctx, promo.ID, user.ID)
	require.NoError(t, err)
	require.Nil(t, got)
}
