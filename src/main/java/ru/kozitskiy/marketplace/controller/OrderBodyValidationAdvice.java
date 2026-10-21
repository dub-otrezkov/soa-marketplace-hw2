package ru.kozitskiy.marketplace.controller;

import java.lang.reflect.Type;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import org.springframework.core.MethodParameter;
import org.springframework.http.HttpInputMessage;
import org.springframework.http.HttpStatus;
import org.springframework.http.converter.HttpMessageConverter;
import org.springframework.web.bind.annotation.ControllerAdvice;
import org.springframework.web.servlet.mvc.method.annotation.RequestBodyAdviceAdapter;
import ru.kozitskiy.marketplace.api.model.OrderCreate;
import ru.kozitskiy.marketplace.api.model.OrderItemRequest;
import ru.kozitskiy.marketplace.api.model.OrderUpdate;
import ru.kozitskiy.marketplace.error.ApiException;

/** Complements generated @Valid: cascading validation alone skips null array elements. */
@ControllerAdvice
public class OrderBodyValidationAdvice extends RequestBodyAdviceAdapter {
    @Override
    public boolean supports(MethodParameter parameter,Type targetType,
                            Class<? extends HttpMessageConverter<?>> converterType) {
        return targetType==OrderCreate.class || targetType==OrderUpdate.class;
    }

    @Override
    public Object afterBodyRead(Object body,HttpInputMessage inputMessage,MethodParameter parameter,
                                Type targetType,Class<? extends HttpMessageConverter<?>> converterType) {
        List<OrderItemRequest> items=body instanceof OrderCreate create?create.getItems():((OrderUpdate)body).getItems();
        // The generated @NotNull/@Size constraints handle a missing/null/empty items property.
        if(items!=null) {
            var violations=new ArrayList<Map<String,String>>();
            for(int i=0;i<items.size();i++) {
                if(items.get(i)==null)
                    violations.add(Map.of("field","items["+i+"]","message","Must be an order item object, not null"));
            }
            if(!violations.isEmpty())
                throw new ApiException(HttpStatus.BAD_REQUEST,"VALIDATION_ERROR","Request validation failed",Map.of("fields",violations));
        }
        return body;
    }
}
