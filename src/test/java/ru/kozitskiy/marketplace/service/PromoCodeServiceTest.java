package ru.kozitskiy.marketplace.service;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import java.math.BigDecimal;
import java.time.OffsetDateTime;
import java.time.ZoneOffset;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.CsvSource;
import org.springframework.jdbc.core.JdbcTemplate;
import ru.kozitskiy.marketplace.error.ApiException;

class PromoCodeServiceTest {
    private final PromoCodeService service = new PromoCodeService(new JdbcTemplate());

    @ParameterizedTest
    @CsvSource({
            "PERCENTAGE, 10.00, 100.00, 10.00",
            "PERCENTAGE, 70.00, 200.00, 140.00",
            "PERCENTAGE, 70.01, 200.00, 0.00",
            "PERCENTAGE, 100.00, 200.00, 0.00",
            "PERCENTAGE, 1000.00, 200.00, 0.00",
            "PERCENTAGE, 25.00, 10.01, 2.50",
            "FIXED_AMOUNT, 20.00, 100.00, 20.00",
            "FIXED_AMOUNT, 200.00, 100.00, 100.00"
    })
    void discountsFollowPercentageLimitAndNeverMakeFixedPriceNegative(
            String type, String value, String subtotal, String expected) {
        var promo = promo(type, value, "0.00", 1, 0, true, -1, 1);
        assertThat(service.discount(promo, new BigDecimal(subtotal)))
                .isEqualByComparingTo(expected)
                .hasScaleOf(2);
    }

    @Test
    void orderCanKeepItsAlreadyReservedLastPromotionUse() {
        var exhausted = promo("PERCENTAGE", "10.00", "0.00", 1, 1, true, -1, 1);
        service.validate(exhausted, true);
        assertThatThrownBy(() -> service.validate(exhausted, false))
                .isInstanceOfSatisfying(ApiException.class,
                        error -> assertThat(error.code()).isEqualTo("PROMO_CODE_INVALID"));
    }

    @ParameterizedTest
    @CsvSource({"false, -1, 1", "true, -2, -1", "true, 1, 2"})
    void existingUseDoesNotBypassActiveOrTimeWindow(boolean active, int startHours, int endHours) {
        var promo = promo("PERCENTAGE", "10.00", "0.00", 1, 1, active, startHours, endHours);
        assertThatThrownBy(() -> service.validate(promo, true))
                .isInstanceOfSatisfying(ApiException.class,
                        error -> assertThat(error.code()).isEqualTo("PROMO_CODE_INVALID"));
    }

    @Test
    void minimumUsesSubtotalAndIncludesExactBoundary() {
        var promo = promo("FIXED_AMOUNT", "20.00", "100.00", 10, 0, true, -1, 1);
        service.requireMinimum(promo, new BigDecimal("100.00"));
        assertThatThrownBy(() -> service.requireMinimum(promo, new BigDecimal("99.99")))
                .isInstanceOfSatisfying(ApiException.class,
                        error -> assertThat(error.code()).isEqualTo("PROMO_CODE_MIN_AMOUNT"));
    }

    private PromoCodeService.Promo promo(String type, String value, String minimum,
                                         int maxUses, int currentUses, boolean active,
                                         int startHours, int endHours) {
        OffsetDateTime now = OffsetDateTime.now(ZoneOffset.UTC);
        return new PromoCodeService.Promo(1L, "TEST_CODE", type, new BigDecimal(value),
                new BigDecimal(minimum), maxUses, currentUses, now.plusHours(startHours),
                now.plusHours(endHours), active);
    }
}
