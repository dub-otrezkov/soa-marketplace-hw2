package ru.kozitskiy.marketplace.error;

import java.util.List;
import java.util.Map;
import jakarta.validation.ConstraintViolationException;
import org.slf4j.LoggerFactory;
import org.springframework.http.HttpStatus;
import org.springframework.http.ResponseEntity;
import org.springframework.http.converter.HttpMessageNotReadableException;
import org.springframework.web.bind.MethodArgumentNotValidException;
import org.springframework.web.bind.annotation.ExceptionHandler;
import org.springframework.web.bind.annotation.RestControllerAdvice;
import org.springframework.web.method.annotation.MethodArgumentTypeMismatchException;
import org.springframework.web.servlet.resource.NoResourceFoundException;
import org.springframework.web.HttpRequestMethodNotSupportedException;
import org.springframework.web.HttpMediaTypeNotSupportedException;
import ru.kozitskiy.marketplace.api.model.ErrorResponse;

@RestControllerAdvice
public class ExceptionHandlerAdvice {
    @ExceptionHandler(ApiException.class)
    ResponseEntity<ErrorResponse> business(ApiException e) { return ResponseEntity.status(e.status()).body(Errors.body(e.code(),e.getMessage(),e.details())); }
    @ExceptionHandler(MethodArgumentNotValidException.class)
    ResponseEntity<ErrorResponse> validation(MethodArgumentNotValidException e) {
        var violations = e.getBindingResult().getFieldErrors().stream().map(v -> Map.of("field",v.getField(),"message",v.getDefaultMessage()==null?"Invalid value":v.getDefaultMessage())).toList();
        return invalid(violations);
    }
    @ExceptionHandler(ConstraintViolationException.class)
    ResponseEntity<ErrorResponse> constraints(ConstraintViolationException e) {
        return invalid(e.getConstraintViolations().stream().map(v -> Map.of("field",v.getPropertyPath().toString(),"message",v.getMessage())).toList());
    }
    @ExceptionHandler(MethodArgumentTypeMismatchException.class)
    ResponseEntity<ErrorResponse> type(MethodArgumentTypeMismatchException e) { return invalid(List.of(Map.of("field",e.getName(),"message","Invalid type or enum value"))); }
    @ExceptionHandler(HttpMessageNotReadableException.class)
    ResponseEntity<ErrorResponse> unreadable(HttpMessageNotReadableException e) {
        String field = "body";
        Throwable cause=e;
        while (cause.getCause()!=null) {
            cause=cause.getCause();
            if (cause instanceof com.fasterxml.jackson.databind.JsonMappingException mapping && !mapping.getPath().isEmpty())
                field=mapping.getPath().stream().map(p -> p.getFieldName()!=null ? p.getFieldName() : "["+p.getIndex()+"]").collect(java.util.stream.Collectors.joining("."));
        }
        return invalid(List.of(Map.of("field",field,"message","Malformed JSON, unknown field or invalid value type")));
    }
    @ExceptionHandler(NoResourceFoundException.class)
    ResponseEntity<ErrorResponse> missing(NoResourceFoundException e) { return ResponseEntity.status(404).body(Errors.body("NOT_FOUND","Endpoint not found",null)); }
    @ExceptionHandler(HttpRequestMethodNotSupportedException.class)
    ResponseEntity<ErrorResponse> method(HttpRequestMethodNotSupportedException e) { return ResponseEntity.status(405).body(Errors.body("METHOD_NOT_ALLOWED","HTTP method not allowed",null)); }
    @ExceptionHandler(HttpMediaTypeNotSupportedException.class)
    ResponseEntity<ErrorResponse> contentType(HttpMediaTypeNotSupportedException e) { return ResponseEntity.status(415).body(Errors.body("UNSUPPORTED_MEDIA_TYPE","Use application/json",null)); }
    @ExceptionHandler(Exception.class)
    ResponseEntity<ErrorResponse> unexpected(Exception e) {
        LoggerFactory.getLogger(getClass()).error("Unhandled API exception",e);
        return ResponseEntity.status(HttpStatus.INTERNAL_SERVER_ERROR).body(Errors.body("INTERNAL_ERROR","Internal server error",null));
    }
    private ResponseEntity<ErrorResponse> invalid(Object details) { return ResponseEntity.badRequest().body(Errors.body("VALIDATION_ERROR","Request validation failed",Map.of("fields",details))); }
}
