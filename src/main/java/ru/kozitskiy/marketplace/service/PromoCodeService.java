package ru.kozitskiy.marketplace.service;

import java.math.BigDecimal;
import java.math.RoundingMode;
import java.time.OffsetDateTime;
import java.time.ZoneOffset;
import java.util.List;
import java.util.Map;
import org.springframework.dao.DuplicateKeyException;
import org.springframework.http.HttpStatus;
import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.stereotype.Service;
import org.springframework.transaction.annotation.Transactional;
import ru.kozitskiy.marketplace.api.model.DiscountType;
import ru.kozitskiy.marketplace.api.model.PromoCodeCreate;
import ru.kozitskiy.marketplace.api.model.PromoCodeResponse;
import ru.kozitskiy.marketplace.error.ApiException;
import ru.kozitskiy.marketplace.security.AuthContext;

@Service
public class PromoCodeService {
    private static final BigDecimal HUNDRED = new BigDecimal("100");
    private static final BigDecimal MAX_PERCENTAGE = new BigDecimal("70");
    private static final BigDecimal ZERO_MONEY = new BigDecimal("0.00");
    private final JdbcTemplate jdbc;

    public PromoCodeService(JdbcTemplate jdbc) {
        this.jdbc = jdbc;
    }

    @Transactional
    public PromoCodeResponse create(PromoCodeCreate request) {
        AuthContext.requireRoles("SELLER", "ADMIN");
        validateMoney("discount_value", request.getDiscountValue());
        validateMoney("min_order_amount", request.getMinOrderAmount());
        if (!request.getValidUntil().isAfter(request.getValidFrom())) {
            throw new ApiException(HttpStatus.BAD_REQUEST, "VALIDATION_ERROR",
                    "The end of the promotion must be after its start",
                    Map.of("fields", List.of(Map.of("field", "valid_until",
                            "message", "must be after valid_from"))));
        }
        Long id;
        try {
            id = jdbc.queryForObject("""
                    INSERT INTO promo_codes
                        (code, discount_type, discount_value, min_order_amount,
                         max_uses, valid_from, valid_until, active)
                    VALUES (?, ?, ?, ?, ?, ?, ?, ?) RETURNING id
                    """, Long.class, request.getCode(), request.getDiscountType().getValue(),
                    request.getDiscountValue(), request.getMinOrderAmount(), request.getMaxUses(),
                    request.getValidFrom(), request.getValidUntil(),
                    request.getActive() == null || request.getActive());
        } catch (DuplicateKeyException exception) {
            throw new ApiException(HttpStatus.CONFLICT, "PROMO_CODE_ALREADY_EXISTS",
                    "A promotion with this code already exists",
                    Map.of("fields", List.of(Map.of("field", "code", "message", "must be unique"))));
        }
        return toResponse(find(id, false));
    }

    /** Called inside the order transaction, after locking its products. */
    Promo lockByCode(String code) {
        return jdbc.query("SELECT * FROM promo_codes WHERE code = ? FOR UPDATE", this::map, code)
                .stream().findFirst().orElseThrow(PromoCodeService::invalidPromo);
    }

    Promo lockById(Long id) {
        return find(id, true);
    }

    private Promo find(Long id, boolean lock) {
        return jdbc.query("SELECT * FROM promo_codes WHERE id = ?" + (lock ? " FOR UPDATE" : ""),
                        this::map, id)
                .stream().findFirst().orElseThrow(PromoCodeService::invalidPromo);
    }

    void validate(Promo promo, boolean alreadyApplied) {
        OffsetDateTime now = OffsetDateTime.now(ZoneOffset.UTC);
        if (!promo.active() || now.isBefore(promo.validFrom()) || now.isAfter(promo.validUntil())
                || (!alreadyApplied && promo.currentUses() >= promo.maxUses())) {
            throw invalidPromo();
        }
    }

    void requireMinimum(Promo promo, BigDecimal subtotal) {
        if (subtotal.compareTo(promo.minOrderAmount()) < 0) {
            throw new ApiException(HttpStatus.UNPROCESSABLE_ENTITY, "PROMO_CODE_MIN_AMOUNT",
                    "Order amount is below the promotion minimum",
                    Map.of("min_order_amount", promo.minOrderAmount(), "order_amount", subtotal));
        }
    }

    BigDecimal discount(Promo promo, BigDecimal subtotal) {
        if ("PERCENTAGE".equals(promo.discountType())) {
            // The assignment says that a percentage above 70 means paying the full price.
            if (promo.discountValue().compareTo(MAX_PERCENTAGE) > 0) {
                return ZERO_MONEY;
            }
            return subtotal.multiply(promo.discountValue()).divide(HUNDRED, 2, RoundingMode.HALF_UP);
        }
        return promo.discountValue().min(subtotal).setScale(2, RoundingMode.HALF_UP);
    }

    void use(Promo promo) {
        jdbc.update("UPDATE promo_codes SET current_uses = current_uses + 1 WHERE id = ?", promo.id());
    }

    void release(Promo promo) {
        jdbc.update("UPDATE promo_codes SET current_uses = current_uses - 1 WHERE id = ?", promo.id());
    }

    private Promo map(java.sql.ResultSet rs, int row) throws java.sql.SQLException {
        return new Promo(rs.getLong("id"), rs.getString("code"), rs.getString("discount_type"),
                rs.getBigDecimal("discount_value"), rs.getBigDecimal("min_order_amount"),
                rs.getInt("max_uses"), rs.getInt("current_uses"),
                rs.getObject("valid_from", OffsetDateTime.class),
                rs.getObject("valid_until", OffsetDateTime.class), rs.getBoolean("active"));
    }

    private PromoCodeResponse toResponse(Promo promo) {
        return new PromoCodeResponse().id(promo.id()).code(promo.code())
                .discountType(DiscountType.fromValue(promo.discountType()))
                .discountValue(promo.discountValue()).minOrderAmount(promo.minOrderAmount())
                .maxUses(promo.maxUses()).currentUses(promo.currentUses())
                .validFrom(promo.validFrom()).validUntil(promo.validUntil()).active(promo.active());
    }

    private static ApiException invalidPromo() {
        return new ApiException(HttpStatus.UNPROCESSABLE_ENTITY, "PROMO_CODE_INVALID",
                "Promotion does not exist, is inactive, expired, not yet valid, or exhausted");
    }

    private void validateMoney(String field, BigDecimal value) {
        if (value.stripTrailingZeros().scale() > 2 || value.compareTo(new BigDecimal("9999999999.99")) > 0) {
            throw new ApiException(HttpStatus.BAD_REQUEST, "VALIDATION_ERROR", "Invalid decimal amount",
                    Map.of("fields", List.of(Map.of("field", field,
                            "message", "must have at most two decimal places and not exceed 9999999999.99"))));
        }
    }

    record Promo(Long id, String code, String discountType, BigDecimal discountValue,
                 BigDecimal minOrderAmount, int maxUses, int currentUses,
                 OffsetDateTime validFrom, OffsetDateTime validUntil, boolean active) {}
}
