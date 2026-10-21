package ru.kozitskiy.marketplace.error;

import java.util.Map;
import ru.kozitskiy.marketplace.api.model.ErrorCode;
import ru.kozitskiy.marketplace.api.model.ErrorResponse;

public final class Errors {
    private Errors() {}
    @SuppressWarnings("unchecked")
    public static ErrorResponse body(String code, String message, Object details) {
        var body = new ErrorResponse();
        body.setErrorCode(ErrorCode.fromValue(code));
        body.setMessage(message);
        if (details != null) body.setDetails(details instanceof Map<?,?> ? (Map<String,Object>)details : Map.of("errors",details));
        return body;
    }
}
