package ru.kozitskiy.marketplace.controller;

import java.util.Arrays;
import java.util.List;
import java.util.Map;
import org.junit.jupiter.api.Test;
import org.springframework.http.HttpStatus;
import ru.kozitskiy.marketplace.api.model.OrderCreate;
import ru.kozitskiy.marketplace.api.model.OrderItemRequest;
import ru.kozitskiy.marketplace.api.model.OrderUpdate;
import ru.kozitskiy.marketplace.error.ApiException;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

class OrderBodyValidationAdviceTest {
    private final OrderBodyValidationAdvice advice=new OrderBodyValidationAdvice();

    @Test
    void rejectsNullArrayElementsInBothRequestSchemas() {
        var item=new OrderItemRequest().productId(1L).quantity(1);
        for(Object body:List.of(new OrderCreate().items(Arrays.asList(item,null)),new OrderUpdate().items(Arrays.asList(item,null)))) {
            assertThatThrownBy(()->advice.afterBodyRead(body,null,null,body.getClass(),null))
                    .isInstanceOfSatisfying(ApiException.class,e->{
                        assertThat(e.status()).isEqualTo(HttpStatus.BAD_REQUEST);
                        assertThat(e.code()).isEqualTo("VALIDATION_ERROR");
                        assertThat(e.details()).isEqualTo(Map.of("fields",List.of(Map.of("field","items[1]","message","Must be an order item object, not null"))));
                    });
        }
    }

    @Test
    void preservesValidGeneratedRequestObjects() {
        var request=new OrderCreate().items(List.of(new OrderItemRequest().productId(1L).quantity(1)));
        assertThat(advice.afterBodyRead(request,null,null,OrderCreate.class,null)).isSameAs(request);
    }
}
