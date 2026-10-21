package server

import (
	"context"
	"errors"
	"math"
	"net/http"
	"slices"
	"time"

	"github.com/dub-otrezkov/soa-marketplace-hw2/internal/api"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// These records describe database rows; all HTTP DTOs are generated from OpenAPI.
type orderRecord struct {
	id        int64
	userID    int64
	status    string
	promoID   *int64
	total     decimal.Decimal
	discount  decimal.Decimal
	createdAt time.Time
	updatedAt time.Time
}

type orderProduct struct {
	id     int64
	price  decimal.Decimal
	stock  int64
	status string
}

type orderItem struct {
	id        int64
	productID int64
	quantity  int32
	price     decimal.Decimal
}

var nextOrderStatus = map[string]string{
	"CREATED": "PAYMENT_PENDING", "PAYMENT_PENDING": "PAID", "PAID": "SHIPPED", "SHIPPED": "COMPLETED",
}

const orderColumns = `id, user_id, status, promo_code_id, total_amount::text,
    discount_amount::text, created_at, updated_at`

func (s *Server) CreateOrder(w http.ResponseWriter, r *http.Request) {
	actor := principal(r)
	if err := requireOrderRole(actor); err != nil {
		writeError(w, err)
		return
	}
	var input api.OrderCreate
	if err := decodeBody(r, &input); err != nil {
		writeError(w, err)
		return
	}
	result, err := s.createOrder(r.Context(), actor, input)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (s *Server) GetOrder(w http.ResponseWriter, r *http.Request, id int64) {
	actor := principal(r)
	if err := requireOrderRole(actor); err != nil {
		writeError(w, err)
		return
	}
	// Both the header and item query must observe the same order revision.
	tx, err := s.pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		writeError(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	order, err := findOrder(r.Context(), tx, id, false)
	if err == nil {
		err = checkOrderOwnership(order, actor)
	}
	var result api.OrderResponse
	if err == nil {
		result, err = orderResponse(r.Context(), tx, order)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) UpdateOrder(w http.ResponseWriter, r *http.Request, id int64) {
	actor := principal(r)
	if err := requireOrderRole(actor); err != nil {
		writeError(w, err)
		return
	}
	var input api.OrderUpdate
	if err := decodeBody(r, &input); err != nil {
		writeError(w, err)
		return
	}
	result, err := s.updateOrder(r.Context(), actor, id, input)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) CancelOrder(w http.ResponseWriter, r *http.Request, id int64) {
	actor := principal(r)
	if err := requireOrderRole(actor); err != nil {
		writeError(w, err)
		return
	}
	result, err := s.cancelOrder(r.Context(), actor, id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) TransitionOrder(w http.ResponseWriter, r *http.Request, id int64) {
	actor := principal(r)
	if actor.Role != "ADMIN" {
		writeError(w, apiError(http.StatusForbidden, "ACCESS_DENIED", "Role does not allow this operation", nil))
		return
	}
	var input api.OrderStatusUpdate
	if err := decodeBody(r, &input); err != nil {
		writeError(w, err)
		return
	}
	result, err := s.transitionOrder(r.Context(), actor, id, string(input.Status))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) createOrder(ctx context.Context, actor Principal, input api.OrderCreate) (api.OrderResponse, error) {
	var result api.OrderResponse
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if err := lockOrderUser(ctx, tx, actor.ID); err != nil {
			return err
		}
		if err := s.checkOrderCooldown(ctx, tx, actor.ID, "CREATE_ORDER"); err != nil {
			return err
		}
		var hasActive bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM orders
            WHERE user_id = $1 AND status IN ('CREATED', 'PAYMENT_PENDING'))`, actor.ID).Scan(&hasActive); err != nil {
			return err
		}
		if hasActive {
			return apiError(http.StatusConflict, "ORDER_HAS_ACTIVE", "The user already has an active order", nil)
		}
		quantities, err := aggregateOrderItems(input.Items)
		if err != nil {
			return err
		}
		products, err := lockOrderProducts(ctx, tx, quantityIDs(quantities))
		if err != nil {
			return err
		}
		if err = checkOrderCatalog(products, quantities); err != nil {
			return err
		}
		if err = checkOrderStock(products, quantities, nil); err != nil {
			return err
		}
		if err = reserveOrderStock(ctx, tx, products, quantities, nil); err != nil {
			return err
		}
		prices := make(map[int64]decimal.Decimal, len(products))
		for id, product := range products {
			prices[id] = product.price
		}
		subtotal := orderSubtotal(quantities, prices)
		var promoID *int64
		discount := decimal.Zero
		if input.PromoCode != nil {
			promo, err := lockPromoByCode(ctx, tx, *input.PromoCode)
			if err != nil {
				return err
			}
			if err = validatePromo(promo, false, time.Now()); err != nil {
				return err
			}
			if err = requirePromoMinimum(promo, subtotal); err != nil {
				return err
			}
			discount = promoDiscount(promo, subtotal)
			if err = usePromo(ctx, tx, promo.id); err != nil {
				return err
			}
			promoID = &promo.id
		}
		total := subtotal.Sub(discount)
		if err = checkOrderMoney(total, discount); err != nil {
			return err
		}
		var id int64
		if err = tx.QueryRow(ctx, `INSERT INTO orders (user_id, status, promo_code_id, total_amount, discount_amount)
            VALUES ($1, 'CREATED', $2, $3, $4) RETURNING id`, actor.ID, promoID, total.String(), discount.String()).Scan(&id); err != nil {
			return err
		}
		if err = insertOrderItems(ctx, tx, id, quantities, prices); err != nil {
			return err
		}
		if err = recordOrderOperation(ctx, tx, actor.ID, "CREATE_ORDER"); err != nil {
			return err
		}
		order, err := findOrder(ctx, tx, id, false)
		if err != nil {
			return err
		}
		result, err = orderResponse(ctx, tx, order)
		return err
	})
	return result, err
}

func (s *Server) updateOrder(ctx context.Context, actor Principal, id int64, input api.OrderUpdate) (api.OrderResponse, error) {
	var result api.OrderResponse
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		order, err := lockOwnedOrder(ctx, tx, id, actor)
		if err != nil {
			return err
		}
		if order.status != "CREATED" {
			return invalidOrderTransition(order.status, "UPDATE")
		}
		if err = s.checkOrderCooldown(ctx, tx, order.userID, "UPDATE_ORDER"); err != nil {
			return err
		}
		oldItems, err := findOrderItems(ctx, tx, id)
		if err != nil {
			return err
		}
		oldQuantities := make(map[int64]int32, len(oldItems))
		oldPrices := make(map[int64]decimal.Decimal, len(oldItems))
		for _, item := range oldItems {
			oldQuantities[item.productID] = item.quantity
			oldPrices[item.productID] = item.price
		}
		quantities, err := aggregateOrderItems(input.Items)
		if err != nil {
			return err
		}
		allIDs := quantityIDs(quantities)
		allIDs = append(allIDs, quantityIDs(oldQuantities)...)
		products, err := lockOrderProducts(ctx, tx, allIDs)
		if err != nil {
			return err
		}
		if err = checkOrderCatalog(products, quantities); err != nil {
			return err
		}
		// Include this order's old reservation in availability, then apply the net
		// change under the product locks. Any later error rolls everything back.
		if err = checkOrderStock(products, quantities, oldQuantities); err != nil {
			return err
		}
		if err = reserveOrderStock(ctx, tx, products, quantities, oldQuantities); err != nil {
			return err
		}
		prices := make(map[int64]decimal.Decimal, len(quantities))
		for productID := range quantities {
			if price, ok := oldPrices[productID]; ok {
				prices[productID] = price
			} else {
				prices[productID] = products[productID].price
			}
		}
		subtotal := orderSubtotal(quantities, prices)
		discount := decimal.Zero
		promoID := order.promoID
		if promoID != nil {
			promo, err := lockPromoByID(ctx, tx, *promoID)
			if err != nil {
				return err
			}
			// This order owns its existing use; do not consume or test another slot.
			if err = validatePromo(promo, true, time.Now()); err != nil {
				return err
			}
			if subtotal.LessThan(promo.minOrderAmount) {
				if err = releasePromo(ctx, tx, promo.id); err != nil {
					return err
				}
				promoID = nil
			} else {
				discount = promoDiscount(promo, subtotal)
			}
		}
		total := subtotal.Sub(discount)
		if err = checkOrderMoney(total, discount); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "DELETE FROM order_items WHERE order_id = $1", id); err != nil {
			return err
		}
		if err = insertOrderItems(ctx, tx, id, quantities, prices); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "UPDATE orders SET promo_code_id = $1, total_amount = $2, discount_amount = $3 WHERE id = $4",
			promoID, total.String(), discount.String(), id); err != nil {
			return err
		}
		if err = recordOrderOperation(ctx, tx, order.userID, "UPDATE_ORDER"); err != nil {
			return err
		}
		order, err = findOrder(ctx, tx, id, false)
		if err != nil {
			return err
		}
		result, err = orderResponse(ctx, tx, order)
		return err
	})
	return result, err
}

func (s *Server) cancelOrder(ctx context.Context, actor Principal, id int64) (api.OrderResponse, error) {
	var result api.OrderResponse
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		order, err := lockOwnedOrder(ctx, tx, id, actor)
		if err != nil {
			return err
		}
		if order.status != "CREATED" && order.status != "PAYMENT_PENDING" {
			return invalidOrderTransition(order.status, "CANCELED")
		}
		items, err := findOrderItems(ctx, tx, id)
		if err != nil {
			return err
		}
		oldQuantities := make(map[int64]int32, len(items))
		for _, item := range items {
			oldQuantities[item.productID] = item.quantity
		}
		products, err := lockOrderProducts(ctx, tx, quantityIDs(oldQuantities))
		if err != nil {
			return err
		}
		if err = reserveOrderStock(ctx, tx, products, nil, oldQuantities); err != nil {
			return err
		}
		if order.promoID != nil {
			promo, err := lockPromoByID(ctx, tx, *order.promoID)
			if err != nil {
				return err
			}
			if err = releasePromo(ctx, tx, promo.id); err != nil {
				return err
			}
		}
		if _, err = tx.Exec(ctx, "UPDATE orders SET status = 'CANCELED' WHERE id = $1", id); err != nil {
			return err
		}
		order, err = findOrder(ctx, tx, id, false)
		if err != nil {
			return err
		}
		result, err = orderResponse(ctx, tx, order)
		return err
	})
	return result, err
}

func (s *Server) transitionOrder(ctx context.Context, actor Principal, id int64, target string) (api.OrderResponse, error) {
	if target == "CANCELED" {
		return s.cancelOrder(ctx, actor, id)
	}
	var result api.OrderResponse
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		order, err := lockOwnedOrder(ctx, tx, id, actor)
		if err != nil {
			return err
		}
		if expected, exists := nextOrderStatus[order.status]; !exists || target != expected {
			return invalidOrderTransition(order.status, target)
		}
		if _, err = tx.Exec(ctx, "UPDATE orders SET status = $1 WHERE id = $2", target, id); err != nil {
			return err
		}
		order, err = findOrder(ctx, tx, id, false)
		if err != nil {
			return err
		}
		result, err = orderResponse(ctx, tx, order)
		return err
	})
	return result, err
}

func lockOwnedOrder(ctx context.Context, tx pgx.Tx, id int64, actor Principal) (orderRecord, error) {
	order, err := findOrder(ctx, tx, id, false)
	if err != nil {
		return order, err
	}
	if err = checkOrderOwnership(order, actor); err != nil {
		return order, err
	}
	// Reading the immutable owner first keeps all writers in the same lock order:
	// user -> order -> sorted products -> promotion.
	if err = lockOrderUser(ctx, tx, order.userID); err != nil {
		return order, err
	}
	order, err = findOrder(ctx, tx, id, true)
	if err != nil {
		return order, err
	}
	return order, checkOrderOwnership(order, actor)
}

func lockOrderUser(ctx context.Context, tx pgx.Tx, id int64) error {
	var locked int64
	// NO KEY UPDATE serializes orders while allowing FK KEY SHARE checks when
	// an administrator concurrently transfers product ownership to this user.
	return tx.QueryRow(ctx, "SELECT id FROM users WHERE id = $1 FOR NO KEY UPDATE", id).Scan(&locked)
}

func requireOrderRole(actor Principal) error {
	if actor.Role != "USER" && actor.Role != "ADMIN" {
		return apiError(http.StatusForbidden, "ACCESS_DENIED", "Role does not allow this operation", nil)
	}
	return nil
}

func checkOrderOwnership(order orderRecord, actor Principal) error {
	if actor.Role != "ADMIN" && actor.ID != order.userID {
		return apiError(http.StatusForbidden, "ORDER_OWNERSHIP_VIOLATION", "This order belongs to another user", nil)
	}
	return nil
}

func (s *Server) checkOrderCooldown(ctx context.Context, tx pgx.Tx, userID int64, operation string) error {
	var previous time.Time
	err := tx.QueryRow(ctx, `SELECT created_at FROM user_operations WHERE user_id = $1 AND operation_type = $2
        ORDER BY created_at DESC, id DESC LIMIT 1`, userID, operation).Scan(&previous)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if s.cfg.OrderCooldown <= 0 {
		return nil
	}
	next := previous.Add(s.cfg.OrderCooldown)
	remaining := time.Until(next)
	if remaining > 0 {
		retrySeconds := int64(math.Ceil(remaining.Seconds()))
		return apiError(http.StatusTooManyRequests, "ORDER_LIMIT_EXCEEDED", "Order operation frequency limit exceeded",
			map[string]any{"operation_type": operation, "retry_after_seconds": retrySeconds})
	}
	return nil
}

func recordOrderOperation(ctx context.Context, tx pgx.Tx, userID int64, operation string) error {
	// CURRENT_TIMESTAMP is transaction start and can precede a long lock wait.
	_, err := tx.Exec(ctx, `INSERT INTO user_operations (user_id, operation_type, created_at)
        VALUES ($1, $2, clock_timestamp())`, userID, operation)
	return err
}

func aggregateOrderItems(items []api.OrderItemRequest) (map[int64]int32, error) {
	quantities := make(map[int64]int32, len(items))
	if len(items) < 1 || len(items) > 50 {
		return nil, apiError(http.StatusBadRequest, "VALIDATION_ERROR", "An order must contain 1 to 50 items",
			map[string]any{"fields": []any{map[string]any{"field": "items", "message": "size must be between 1 and 50"}}})
	}
	for index, item := range items {
		if item.ProductId < 1 || item.Quantity < 1 || item.Quantity > 999 {
			return nil, apiError(http.StatusBadRequest, "VALIDATION_ERROR", "Invalid order item",
				map[string]any{"fields": []any{map[string]any{"field": "items", "index": index,
					"message": "product_id must be positive and quantity between 1 and 999"}}})
		}
		quantity := quantities[item.ProductId] + item.Quantity
		if quantity > 999 {
			return nil, apiError(http.StatusBadRequest, "VALIDATION_ERROR", "The total quantity of one product must not exceed 999",
				map[string]any{"fields": []any{map[string]any{"field": "items", "product_id": item.ProductId,
					"message": "combined quantity must be at most 999"}}})
		}
		quantities[item.ProductId] = quantity
	}
	return quantities, nil
}

func quantityIDs(quantities map[int64]int32) []int64 {
	ids := make([]int64, 0, len(quantities))
	for id := range quantities {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func lockOrderProducts(ctx context.Context, tx pgx.Tx, ids []int64) (map[int64]orderProduct, error) {
	products := make(map[int64]orderProduct, len(ids))
	// Sorting is mandatory even when callers combine old and new item lists.
	ids = slices.Clone(ids)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	for _, id := range ids {
		var product orderProduct
		var price string
		err := tx.QueryRow(ctx, "SELECT id, price::text, stock, status FROM products WHERE id = $1 FOR UPDATE", id).
			Scan(&product.id, &price, &product.stock, &product.status)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apiError(http.StatusNotFound, "PRODUCT_NOT_FOUND", "Product not found", map[string]any{"product_id": id})
		}
		if err != nil {
			return nil, err
		}
		product.price, err = decimal.NewFromString(price)
		if err != nil {
			return nil, err
		}
		products[id] = product
	}
	return products, nil
}

func checkOrderCatalog(products map[int64]orderProduct, quantities map[int64]int32) error {
	for _, id := range quantityIDs(quantities) {
		if products[id].status != "ACTIVE" {
			return apiError(http.StatusConflict, "PRODUCT_INACTIVE", "Product is not active", map[string]any{"product_id": id})
		}
	}
	return nil
}

func checkOrderStock(products map[int64]orderProduct, quantities, oldQuantities map[int64]int32) error {
	shortages := make([]any, 0)
	for _, id := range quantityIDs(quantities) {
		available := products[id].stock + int64(oldQuantities[id])
		if available < int64(quantities[id]) {
			shortages = append(shortages, map[string]any{
				"product_id": id, "requested_quantity": quantities[id], "available_stock": available,
			})
		}
	}
	if len(shortages) > 0 {
		return apiError(http.StatusConflict, "INSUFFICIENT_STOCK", "Insufficient product stock",
			map[string]any{"products": shortages})
	}
	return nil
}

func reserveOrderStock(ctx context.Context, tx pgx.Tx, products map[int64]orderProduct,
	quantities, oldQuantities map[int64]int32) error {
	ids := make([]int64, 0, len(products))
	for id := range products {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		delta := int64(oldQuantities[id]) - int64(quantities[id])
		// Replenishment may occur between reservation and cancellation.
		if products[id].stock+delta > math.MaxInt32 {
			return apiError(http.StatusBadRequest, "VALIDATION_ERROR", "Restoring reserved items exceeds the maximum supported stock",
				map[string]any{"fields": []any{map[string]any{"field": "stock", "product_id": id,
					"message": "restored stock must not exceed 2147483647"}}})
		}
		if delta != 0 {
			if _, err := tx.Exec(ctx, "UPDATE products SET stock = stock + $1 WHERE id = $2", delta, id); err != nil {
				return err
			}
		}
	}
	return nil
}

func orderSubtotal(quantities map[int64]int32, prices map[int64]decimal.Decimal) decimal.Decimal {
	total := decimal.Zero
	for id, quantity := range quantities {
		total = total.Add(prices[id].Mul(decimal.NewFromInt(int64(quantity))))
	}
	return total.Round(2)
}

func checkOrderMoney(total, discount decimal.Decimal) error {
	maximum := decimal.RequireFromString("9999999999.99")
	if total.GreaterThan(maximum) || discount.GreaterThan(maximum) {
		return apiError(http.StatusBadRequest, "VALIDATION_ERROR", "Order amount is too large",
			map[string]any{"fields": []any{map[string]any{"field": "items", "message": "order amount exceeds 9999999999.99"}}})
	}
	return nil
}

func insertOrderItems(ctx context.Context, tx pgx.Tx, orderID int64,
	quantities map[int64]int32, prices map[int64]decimal.Decimal) error {
	for _, id := range quantityIDs(quantities) {
		if _, err := tx.Exec(ctx, `INSERT INTO order_items (order_id, product_id, quantity, price_at_order)
            VALUES ($1, $2, $3, $4)`, orderID, id, quantities[id], prices[id].String()); err != nil {
			return err
		}
	}
	return nil
}

func findOrder(ctx context.Context, tx pgx.Tx, id int64, lock bool) (orderRecord, error) {
	query := "SELECT " + orderColumns + " FROM orders WHERE id = $1"
	if lock {
		query += " FOR UPDATE"
	}
	var order orderRecord
	var total, discount string
	err := tx.QueryRow(ctx, query, id).Scan(&order.id, &order.userID, &order.status, &order.promoID,
		&total, &discount, &order.createdAt, &order.updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return order, apiError(http.StatusNotFound, "ORDER_NOT_FOUND", "Order not found", map[string]any{"order_id": id})
	}
	if err != nil {
		return order, err
	}
	order.total, err = decimal.NewFromString(total)
	if err != nil {
		return order, err
	}
	order.discount, err = decimal.NewFromString(discount)
	return order, err
}

func findOrderItems(ctx context.Context, tx pgx.Tx, id int64) ([]orderItem, error) {
	rows, err := tx.Query(ctx, `SELECT id, product_id, quantity, price_at_order::text
        FROM order_items WHERE order_id = $1 ORDER BY product_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]orderItem, 0)
	for rows.Next() {
		var item orderItem
		var price string
		if err := rows.Scan(&item.id, &item.productID, &item.quantity, &price); err != nil {
			return nil, err
		}
		item.price, err = decimal.NewFromString(price)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func orderResponse(ctx context.Context, tx pgx.Tx, order orderRecord) (api.OrderResponse, error) {
	items, err := findOrderItems(ctx, tx, order.id)
	if err != nil {
		return api.OrderResponse{}, err
	}
	responseItems := make([]api.OrderItemResponse, 0, len(items))
	for _, item := range items {
		responseItems = append(responseItems, api.OrderItemResponse{
			Id: item.id, ProductId: item.productID, Quantity: item.quantity, PriceAtOrder: item.price,
		})
	}
	return api.OrderResponse{
		Id: order.id, UserId: order.userID, Status: api.OrderStatus(order.status), PromoCodeId: order.promoID,
		TotalAmount: order.total, DiscountAmount: order.discount,
		CreatedAt: order.createdAt, UpdatedAt: order.updatedAt, Items: responseItems,
	}, nil
}

func invalidOrderTransition(current, target string) error {
	return apiError(http.StatusConflict, "INVALID_STATE_TRANSITION", "This operation is not allowed in the current order state",
		map[string]any{"current_status": current, "requested_status": target})
}
