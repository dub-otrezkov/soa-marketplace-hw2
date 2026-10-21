package ru.kozitskiy.marketplace.service;

import java.math.BigDecimal;
import java.math.RoundingMode;
import java.time.Duration;
import java.time.OffsetDateTime;
import java.time.ZoneOffset;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.TreeMap;
import java.util.TreeSet;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.http.HttpStatus;
import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.stereotype.Service;
import org.springframework.transaction.annotation.Isolation;
import org.springframework.transaction.annotation.Transactional;
import ru.kozitskiy.marketplace.api.model.OrderCreate;
import ru.kozitskiy.marketplace.api.model.OrderItemRequest;
import ru.kozitskiy.marketplace.api.model.OrderItemResponse;
import ru.kozitskiy.marketplace.api.model.OrderResponse;
import ru.kozitskiy.marketplace.api.model.OrderStatus;
import ru.kozitskiy.marketplace.api.model.OrderStatusUpdate;
import ru.kozitskiy.marketplace.api.model.OrderUpdate;
import ru.kozitskiy.marketplace.error.ApiException;
import ru.kozitskiy.marketplace.security.AuthContext;

@Service
public class OrderService {
    private static final BigDecimal ZERO_MONEY = new BigDecimal("0.00");
    private static final BigDecimal MAX_MONEY = new BigDecimal("9999999999.99");
    private static final Map<String, String> NEXT_STATUS = Map.of(
            "CREATED", "PAYMENT_PENDING", "PAYMENT_PENDING", "PAID",
            "PAID", "SHIPPED", "SHIPPED", "COMPLETED");
    private final JdbcTemplate jdbc;
    private final PromoCodeService promos;
    private final Duration cooldown;

    public OrderService(JdbcTemplate jdbc, PromoCodeService promos,
                        @Value("${marketplace.order-cooldown-minutes:5}") long cooldownMinutes) {
        if (cooldownMinutes < 0) {
            throw new IllegalArgumentException("Order cooldown must be nonnegative");
        }
        this.jdbc = jdbc;
        this.promos = promos;
        this.cooldown = Duration.ofMinutes(cooldownMinutes);
    }

    @Transactional
    public OrderResponse create(OrderCreate request) {
        AuthContext.requireRoles("USER", "ADMIN");
        Long userId = AuthContext.current().id();
        // This lock also serializes operations for a user who has no orders yet.
        lockUser(userId);
        checkCooldown(userId, "CREATE_ORDER");
        Boolean hasActive = jdbc.queryForObject("""
                SELECT EXISTS (SELECT 1 FROM orders
                WHERE user_id = ? AND status IN ('CREATED', 'PAYMENT_PENDING'))
                """, Boolean.class, userId);
        if (Boolean.TRUE.equals(hasActive)) {
            throw new ApiException(HttpStatus.CONFLICT, "ORDER_HAS_ACTIVE",
                    "The user already has an active order");
        }
        Map<Long, Integer> quantities = aggregate(request.getItems());
        Map<Long, Product> products = lockProducts(quantities.keySet());
        checkCatalog(products, quantities.keySet());
        checkStock(products, quantities, Map.of());
        Map<Long, BigDecimal> prices = new TreeMap<>();
        products.forEach((id, product) -> prices.put(id, product.price()));
        reserve(products, quantities, Map.of());
        BigDecimal subtotal = subtotal(quantities, prices);
        PromoCodeService.Promo promo = null;
        BigDecimal discount = ZERO_MONEY;
        if (request.getPromoCode() != null) {
            promo = promos.lockByCode(request.getPromoCode());
            promos.validate(promo, false);
            promos.requireMinimum(promo, subtotal);
            discount = promos.discount(promo, subtotal);
            promos.use(promo);
        }
        BigDecimal total = subtotal.subtract(discount);
        checkMoney(total, discount);
        Long orderId = jdbc.queryForObject("""
                INSERT INTO orders (user_id, status, promo_code_id, total_amount, discount_amount)
                VALUES (?, 'CREATED', ?, ?, ?) RETURNING id
                """, Long.class, userId, promo == null ? null : promo.id(), total, discount);
        insertItems(orderId, quantities, prices);
        recordOperation(userId, "CREATE_ORDER");
        return response(findOrder(orderId, false));
    }

    @Transactional(readOnly = true, isolation = Isolation.REPEATABLE_READ)
    public OrderResponse get(Long id) {
        AuthContext.requireRoles("USER", "ADMIN");
        Order order = findOrder(id, false);
        checkOwnership(order);
        return response(order);
    }

    @Transactional
    public OrderResponse update(Long id, OrderUpdate request) {
        AuthContext.requireRoles("USER", "ADMIN");
        Order order = lockOwnedOrder(id);
        if (!"CREATED".equals(order.status())) {
            throw invalidTransition(order.status(), "UPDATE");
        }
        checkCooldown(order.userId(), "UPDATE_ORDER");
        Map<Long, Item> oldItems = itemsByProduct(id);
        Map<Long, Integer> oldQuantities = new TreeMap<>();
        oldItems.forEach((productId, item) -> oldQuantities.put(productId, item.quantity()));
        Map<Long, Integer> quantities = aggregate(request.getItems());
        Set<Long> allIds = new TreeSet<>(oldQuantities.keySet());
        allIds.addAll(quantities.keySet());
        Map<Long, Product> products = lockProducts(allIds);
        checkCatalog(products, quantities.keySet());
        // The available amount includes this order's previous reservation. Applying
        // the net change under the product locks is equivalent to return + reserve.
        checkStock(products, quantities, oldQuantities);
        reserve(products, quantities, oldQuantities);
        Map<Long, BigDecimal> prices = new TreeMap<>();
        quantities.keySet().forEach(productId -> prices.put(productId,
                oldItems.containsKey(productId) ? oldItems.get(productId).price() : products.get(productId).price()));
        BigDecimal subtotal = subtotal(quantities, prices);
        Long promoId = order.promoId();
        BigDecimal discount = ZERO_MONEY;
        if (promoId != null) {
            PromoCodeService.Promo promo = promos.lockById(promoId);
            // Its current use already belongs to this order, so capacity is not re-consumed.
            promos.validate(promo, true);
            if (subtotal.compareTo(promo.minOrderAmount()) < 0) {
                promos.release(promo);
                promoId = null;
            } else {
                discount = promos.discount(promo, subtotal);
            }
        }
        BigDecimal total = subtotal.subtract(discount);
        checkMoney(total, discount);
        jdbc.update("DELETE FROM order_items WHERE order_id = ?", id);
        insertItems(id, quantities, prices);
        jdbc.update("UPDATE orders SET promo_code_id = ?, total_amount = ?, discount_amount = ? WHERE id = ?",
                promoId, total, discount, id);
        recordOperation(order.userId(), "UPDATE_ORDER");
        return response(findOrder(id, false));
    }

    @Transactional
    public OrderResponse cancel(Long id) {
        AuthContext.requireRoles("USER", "ADMIN");
        Order order = lockOwnedOrder(id);
        if (!Set.of("CREATED", "PAYMENT_PENDING").contains(order.status())) {
            throw invalidTransition(order.status(), "CANCELED");
        }
        Map<Long, Item> items = itemsByProduct(id);
        Map<Long, Product> products = lockProducts(items.keySet());
        Map<Long, Integer> oldQuantities = new TreeMap<>();
        items.forEach((productId, item) -> oldQuantities.put(productId, item.quantity()));
        reserve(products, Map.of(), oldQuantities);
        if (order.promoId() != null) {
            promos.release(promos.lockById(order.promoId()));
        }
        jdbc.update("UPDATE orders SET status = 'CANCELED' WHERE id = ?", id);
        return response(findOrder(id, false));
    }

    @Transactional
    public OrderResponse transition(Long id, OrderStatusUpdate request) {
        AuthContext.requireRoles("ADMIN");
        String target = request.getStatus().getValue();
        if ("CANCELED".equals(target)) {
            return cancel(id);
        }
        Order order = lockOwnedOrder(id);
        if (!target.equals(NEXT_STATUS.get(order.status()))) {
            throw invalidTransition(order.status(), target);
        }
        jdbc.update("UPDATE orders SET status = ? WHERE id = ?", target, id);
        return response(findOrder(id, false));
    }

    private Order lockOwnedOrder(Long id) {
        Order initial = findOrder(id, false);
        checkOwnership(initial);
        // All mutation paths acquire user -> order -> sorted products -> promotion.
        // Reading the immutable owner first avoids the inverse order -> user lock order.
        lockUser(initial.userId());
        Order locked = findOrder(id, true);
        checkOwnership(locked);
        return locked;
    }

    private void lockUser(Long id) {
        // Serialize this user's order operations without blocking FK KEY SHARE checks
        // when an administrator concurrently transfers ownership of a product.
        jdbc.queryForObject("SELECT id FROM users WHERE id = ? FOR NO KEY UPDATE", Long.class, id);
    }

    private void checkOwnership(Order order) {
        var actor = AuthContext.current();
        if (!actor.isAdmin() && !actor.id().equals(order.userId())) {
            throw new ApiException(HttpStatus.FORBIDDEN, "ORDER_OWNERSHIP_VIOLATION",
                    "This order belongs to another user");
        }
    }

    private void checkCooldown(Long userId, String operation) {
        List<OffsetDateTime> previous = jdbc.query("""
                SELECT created_at FROM user_operations WHERE user_id = ? AND operation_type = ?
                ORDER BY created_at DESC, id DESC LIMIT 1
                """, (rs, row) -> rs.getObject("created_at", OffsetDateTime.class), userId, operation);
        if (previous.isEmpty() || cooldown.isZero()) {
            return;
        }
        OffsetDateTime now = OffsetDateTime.now(ZoneOffset.UTC);
        OffsetDateTime next = previous.getFirst().plus(cooldown);
        if (now.isBefore(next)) {
            long retrySeconds = Math.max(1L, (Duration.between(now, next).toMillis() + 999L) / 1000L);
            throw new ApiException(HttpStatus.TOO_MANY_REQUESTS, "ORDER_LIMIT_EXCEEDED",
                    "Order operation frequency limit exceeded",
                    Map.of("operation_type", operation, "retry_after_seconds", retrySeconds));
        }
    }

    private void recordOperation(Long userId, String type) {
        // PostgreSQL CURRENT_TIMESTAMP is the transaction's start, which may precede
        // a wait for the user lock. Record the successful operation's actual time.
        jdbc.update("INSERT INTO user_operations (user_id, operation_type, created_at) VALUES (?, ?, clock_timestamp())",
                userId, type);
    }

    private Map<Long, Integer> aggregate(List<OrderItemRequest> requests) {
        Map<Long, Integer> quantities = new TreeMap<>();
        for (OrderItemRequest item : requests) {
            int quantity = quantities.getOrDefault(item.getProductId(), 0) + item.getQuantity();
            if (quantity > 999) {
                throw new ApiException(HttpStatus.BAD_REQUEST, "VALIDATION_ERROR",
                        "The total quantity of one product must not exceed 999",
                        Map.of("fields", List.of(Map.of("field", "items", "product_id", item.getProductId(),
                                "message", "combined quantity must be at most 999"))));
            }
            quantities.put(item.getProductId(), quantity);
        }
        return quantities;
    }

    private Map<Long, Product> lockProducts(Set<Long> ids) {
        Map<Long, Product> result = new TreeMap<>();
        // A deterministic order prevents deadlocks for orders containing the same products.
        for (Long id : new TreeSet<>(ids)) {
            List<Product> found = jdbc.query("SELECT id, price, stock, status FROM products WHERE id = ? FOR UPDATE",
                    (rs, row) -> new Product(rs.getLong("id"), rs.getBigDecimal("price"),
                            rs.getInt("stock"), rs.getString("status")), id);
            if (found.isEmpty()) {
                throw new ApiException(HttpStatus.NOT_FOUND, "PRODUCT_NOT_FOUND", "Product not found",
                        Map.of("product_id", id));
            }
            result.put(id, found.getFirst());
        }
        return result;
    }

    private void checkCatalog(Map<Long, Product> products, Set<Long> requestedIds) {
        for (Long id : requestedIds) {
            if (!"ACTIVE".equals(products.get(id).status())) {
                throw new ApiException(HttpStatus.CONFLICT, "PRODUCT_INACTIVE", "Product is not active",
                        Map.of("product_id", id));
            }
        }
    }

    private void checkStock(Map<Long, Product> products, Map<Long, Integer> quantities,
                            Map<Long, Integer> oldQuantities) {
        List<Map<String, Object>> shortages = new ArrayList<>();
        quantities.forEach((id, quantity) -> {
            long available = (long) products.get(id).stock() + oldQuantities.getOrDefault(id, 0);
            if (available < quantity) {
                shortages.add(Map.of("product_id", id, "requested_quantity", quantity, "available_stock", available));
            }
        });
        if (!shortages.isEmpty()) {
            throw new ApiException(HttpStatus.CONFLICT, "INSUFFICIENT_STOCK", "Insufficient product stock",
                    Map.of("products", shortages));
        }
    }

    private void reserve(Map<Long, Product> products, Map<Long, Integer> quantities,
                         Map<Long, Integer> oldQuantities) {
        for (Long id : new TreeSet<>(products.keySet())) {
            int delta = oldQuantities.getOrDefault(id, 0) - quantities.getOrDefault(id, 0);
            // A seller can replenish stock between reservation and cancellation.
            // Surface the integer boundary as a contract error instead of a DB overflow.
            if ((long) products.get(id).stock() + delta > Integer.MAX_VALUE) {
                throw new ApiException(HttpStatus.BAD_REQUEST, "VALIDATION_ERROR",
                        "Restoring reserved items exceeds the maximum supported stock",
                        Map.of("fields", List.of(Map.of("field", "stock", "product_id", id,
                                "message", "restored stock must not exceed 2147483647"))));
            }
            if (delta != 0) {
                jdbc.update("UPDATE products SET stock = stock + ? WHERE id = ?", delta, id);
            }
        }
    }

    private BigDecimal subtotal(Map<Long, Integer> quantities, Map<Long, BigDecimal> prices) {
        BigDecimal subtotal = ZERO_MONEY;
        for (Map.Entry<Long, Integer> entry : quantities.entrySet()) {
            subtotal = subtotal.add(prices.get(entry.getKey()).multiply(BigDecimal.valueOf(entry.getValue())));
        }
        return subtotal.setScale(2, RoundingMode.HALF_UP);
    }

    private void checkMoney(BigDecimal total, BigDecimal discount) {
        if (total.compareTo(MAX_MONEY) > 0 || discount.compareTo(MAX_MONEY) > 0) {
            throw new ApiException(HttpStatus.BAD_REQUEST, "VALIDATION_ERROR", "Order amount is too large",
                    Map.of("fields", List.of(Map.of("field", "items", "message", "order amount exceeds 9999999999.99"))));
        }
    }

    private void insertItems(Long orderId, Map<Long, Integer> quantities, Map<Long, BigDecimal> prices) {
        quantities.forEach((id, quantity) -> jdbc.update("""
                INSERT INTO order_items (order_id, product_id, quantity, price_at_order) VALUES (?, ?, ?, ?)
                """, orderId, id, quantity, prices.get(id)));
    }

    private Order findOrder(Long id, boolean lock) {
        return jdbc.query("SELECT * FROM orders WHERE id = ?" + (lock ? " FOR UPDATE" : ""),
                        (rs, row) -> new Order(rs.getLong("id"), rs.getLong("user_id"), rs.getString("status"),
                                rs.getObject("promo_code_id", Long.class), rs.getBigDecimal("total_amount"),
                                rs.getBigDecimal("discount_amount"), rs.getObject("created_at", OffsetDateTime.class),
                                rs.getObject("updated_at", OffsetDateTime.class)), id)
                .stream().findFirst().orElseThrow(() -> new ApiException(HttpStatus.NOT_FOUND,
                        "ORDER_NOT_FOUND", "Order not found", Map.of("order_id", id)));
    }

    private Map<Long, Item> itemsByProduct(Long orderId) {
        Map<Long, Item> items = new LinkedHashMap<>();
        jdbc.query("SELECT * FROM order_items WHERE order_id = ? ORDER BY product_id",
                (rs, row) -> new Item(rs.getLong("id"), rs.getLong("product_id"), rs.getInt("quantity"),
                        rs.getBigDecimal("price_at_order")), orderId)
                .forEach(item -> items.put(item.productId(), item));
        return items;
    }

    private OrderResponse response(Order order) {
        List<OrderItemResponse> items = itemsByProduct(order.id()).values().stream()
                .map(item -> new OrderItemResponse().id(item.id()).productId(item.productId())
                        .quantity(item.quantity()).priceAtOrder(item.price()))
                .toList();
        return new OrderResponse().id(order.id()).userId(order.userId())
                .status(OrderStatus.fromValue(order.status())).promoCodeId(order.promoId())
                .totalAmount(order.total()).discountAmount(order.discount())
                .createdAt(order.createdAt()).updatedAt(order.updatedAt()).items(items);
    }

    private ApiException invalidTransition(String current, String target) {
        return new ApiException(HttpStatus.CONFLICT, "INVALID_STATE_TRANSITION",
                "This operation is not allowed in the current order state",
                Map.of("current_status", current, "requested_status", target));
    }

    private record Product(Long id, BigDecimal price, int stock, String status) {}
    private record Item(Long id, Long productId, int quantity, BigDecimal price) {}
    private record Order(Long id, Long userId, String status, Long promoId, BigDecimal total,
                         BigDecimal discount, OffsetDateTime createdAt, OffsetDateTime updatedAt) {}
}
