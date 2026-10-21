package ru.kozitskiy.marketplace.controller;

import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.RestController;
import ru.kozitskiy.marketplace.api.AuthApi;
import ru.kozitskiy.marketplace.api.model.*;
import ru.kozitskiy.marketplace.service.AuthService;

@RestController
public class AuthController implements AuthApi {
    private final AuthService service;
    public AuthController(AuthService service) { this.service=service; }
    @Override public ResponseEntity<UserResponse> register(RegisterRequest request) { return ResponseEntity.status(201).body(service.register(request)); }
    @Override public ResponseEntity<TokenResponse> login(LoginRequest request) { return ResponseEntity.ok(service.login(request)); }
    @Override public ResponseEntity<TokenResponse> refresh(RefreshRequest request) { return ResponseEntity.ok(service.refresh(request)); }
}
