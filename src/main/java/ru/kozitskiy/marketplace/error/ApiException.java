package ru.kozitskiy.marketplace.error;

import org.springframework.http.HttpStatus;

public class ApiException extends RuntimeException {
    private final HttpStatus status;
    private final String code;
    private final Object details;
    public ApiException(HttpStatus status, String code, String message) { this(status,code,message,null); }
    public ApiException(HttpStatus status, String code, String message, Object details) {
        super(message); this.status=status; this.code=code; this.details=details;
    }
    public HttpStatus status() { return status; }
    public String code() { return code; }
    public Object details() { return details; }
}
