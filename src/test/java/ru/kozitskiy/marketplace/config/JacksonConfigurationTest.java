package ru.kozitskiy.marketplace.config;

import java.math.BigDecimal;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.exc.MismatchedInputException;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.ValueSource;
import org.springframework.http.converter.json.Jackson2ObjectMapperBuilder;
import ru.kozitskiy.marketplace.api.model.LoginRequest;
import ru.kozitskiy.marketplace.api.model.ProductCreate;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

class JacksonConfigurationTest {
    private final ObjectMapper mapper=mapper();

    @ParameterizedTest
    @ValueSource(strings={"123","12.5","true"})
    void rejectsNonStringProductNamesAndPasswords(String jsonValue) {
        assertThatThrownBy(()->mapper.readValue("{\"name\":"+jsonValue+"}",ProductCreate.class))
                .isInstanceOf(MismatchedInputException.class);
        assertThatThrownBy(()->mapper.readValue("{\"password\":"+jsonValue+"}",LoginRequest.class))
                .isInstanceOf(MismatchedInputException.class);
    }

    @Test
    void retainsStringsAndNativeNumericFields() throws Exception {
        var product=mapper.readValue("{\"name\":\"Model 123\",\"price\":12.50,\"stock\":3,\"category\":\"Books\",\"status\":\"ACTIVE\"}",ProductCreate.class);
        assertThat(product.getName()).isEqualTo("Model 123");
        assertThat(product.getPrice()).isEqualByComparingTo(new BigDecimal("12.50"));
        assertThat(product.getStock()).isEqualTo(3);
    }

    private static ObjectMapper mapper() {
        var builder=new Jackson2ObjectMapperBuilder();
        new JacksonConfiguration().strictTextValues().customize(builder);
        return builder.build();
    }
}
