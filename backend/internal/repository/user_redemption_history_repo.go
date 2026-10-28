package repository

import (
	"context"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/promocodeusage"
	"github.com/Wei-Shaw/sub2api/ent/redeemcode"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type userRedemptionHistoryRepository struct{ client *dbent.Client }

func NewUserRedemptionHistoryRepository(client *dbent.Client) service.UserRedemptionHistoryRepository {
	return &userRedemptionHistoryRepository{client: client}
}

// ListByUser pages over both sources in SQL before hydrating the page. This
// preserves global ordering without loading an entire user's history. The
// total comes from the page query itself so it always matches the page.
func (r *userRedemptionHistoryRepository) ListByUser(ctx context.Context, userID int64, params pagination.PaginationParams) ([]service.RedeemCode, *pagination.PaginationResult, error) {
	client := clientFromContext(ctx, r.client)
	// The ordering matches redeemCodeRepository.ListByUser for redeem-only
	// histories; source breaks ties between the two ID spaces.
	rows, err := client.QueryContext(ctx, `
SELECT id, source, COUNT(*) OVER () FROM (
    SELECT id, 'redeem' AS source, used_at FROM redeem_codes WHERE used_by = $1
    UNION ALL
    SELECT id, 'promo' AS source, used_at FROM promo_code_usages WHERE user_id = $1
) AS history
ORDER BY used_at DESC, source ASC, id DESC
LIMIT $2 OFFSET $3`, userID, params.Limit(), params.Offset())
	if err != nil {
		return nil, nil, err
	}
	type historyKey struct {
		id     int64
		source string
	}
	var keys []historyKey
	var redeemIDs, promoIDs []int64
	var total int64
	for rows.Next() {
		var key historyKey
		if err := rows.Scan(&key.id, &key.source, &total); err != nil {
			rows.Close()
			return nil, nil, err
		}
		keys = append(keys, key)
		if key.source == "promo" {
			promoIDs = append(promoIDs, key.id)
		} else {
			redeemIDs = append(redeemIDs, key.id)
		}
	}
	rowErr := rows.Err()
	rows.Close()
	if rowErr != nil {
		return nil, nil, rowErr
	}
	// A page past the end has no rows to carry the window count.
	if len(keys) == 0 && params.Offset() > 0 {
		redeemTotal, err := client.RedeemCode.Query().Where(redeemcode.UsedByEQ(userID)).Count(ctx)
		if err != nil {
			return nil, nil, err
		}
		promoTotal, err := client.PromoCodeUsage.Query().Where(promocodeusage.UserIDEQ(userID)).Count(ctx)
		if err != nil {
			return nil, nil, err
		}
		total = int64(redeemTotal) + int64(promoTotal)
	}
	result := paginationResultFromTotal(total, params)

	items := make(map[historyKey]service.RedeemCode, len(keys))
	if len(redeemIDs) > 0 {
		codes, err := client.RedeemCode.Query().Where(redeemcode.IDIn(redeemIDs...), redeemcode.UsedByEQ(userID)).WithGroup().All(ctx)
		if err != nil {
			return nil, nil, err
		}
		for _, code := range codes {
			items[historyKey{code.ID, "redeem"}] = *redeemCodeEntityToService(code)
		}
	}
	if len(promoIDs) > 0 {
		usages, err := client.PromoCodeUsage.Query().Where(promocodeusage.IDIn(promoIDs...), promocodeusage.UserIDEQ(userID)).WithPromoCode().All(ctx)
		if err != nil {
			return nil, nil, err
		}
		for _, usage := range usages {
			u := promoCodeUsageEntityToService(usage)
			u.PromoCode = promoCodeEntityToService(usage.Edges.PromoCode)
			items[historyKey{usage.ID, "promo"}] = *service.PromoUsageAsRedemption(u)
		}
	}
	out := make([]service.RedeemCode, 0, len(keys))
	for _, key := range keys {
		if item, ok := items[key]; ok {
			out = append(out, item)
		}
	}
	return out, result, nil
}
