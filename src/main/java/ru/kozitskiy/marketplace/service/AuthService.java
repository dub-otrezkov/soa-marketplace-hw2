package ru.kozitskiy.marketplace.service;

import java.nio.charset.StandardCharsets;
import java.util.Locale;
import java.util.UUID;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.ApplicationArguments;
import org.springframework.boot.ApplicationRunner;
import org.springframework.http.HttpStatus;
import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.security.crypto.password.PasswordEncoder;
import org.springframework.stereotype.Service;
import org.springframework.transaction.annotation.Transactional;
import ru.kozitskiy.marketplace.api.model.*;
import ru.kozitskiy.marketplace.error.ApiException;
import ru.kozitskiy.marketplace.security.TokenService;

@Service
public class AuthService implements ApplicationRunner {
    private final JdbcTemplate jdbc;
    private final PasswordEncoder passwords;
    private final TokenService tokens;
    private final String adminEmail,adminPassword;
    private final String dummyHash;
    public AuthService(JdbcTemplate jdbc,PasswordEncoder passwords,TokenService tokens,
            @Value("${marketplace.bootstrap-admin-email}") String adminEmail,
            @Value("${marketplace.bootstrap-admin-password}") String adminPassword) {
        this.jdbc=jdbc; this.passwords=passwords; this.tokens=tokens; this.adminEmail=adminEmail; this.adminPassword=adminPassword;
        dummyHash=passwords.encode(UUID.randomUUID().toString());
    }
    @Override public void run(ApplicationArguments args) {
        if(!adminEmail.isBlank() && !adminPassword.isBlank()) {
            if(adminPassword.length()<12) throw new IllegalArgumentException("Bootstrap administrator password must have at least 12 characters");
            if(adminPassword.getBytes(StandardCharsets.UTF_8).length>72)
                throw new IllegalArgumentException("Bootstrap administrator password must contain at most 72 UTF-8 bytes");
            jdbc.update("INSERT INTO users(email,password_hash,role) VALUES (?,?,'ADMIN') ON CONFLICT(email) DO NOTHING",normalize(adminEmail),passwords.encode(adminPassword));
        }
    }
    @Transactional
    public UserResponse register(RegisterRequest request) {
        validatePasswordBytes(request.getPassword());
        var role=request.getRole()==null?Role.USER:request.getRole();
        if(role==Role.ADMIN) throw new ApiException(HttpStatus.BAD_REQUEST,"VALIDATION_ERROR","ADMIN cannot be registered through the public API",java.util.Map.of("field","role"));
        var ids=jdbc.query("INSERT INTO users(email,password_hash,role) VALUES (?,?,?) ON CONFLICT(email) DO NOTHING RETURNING id",(rs,n)->rs.getLong(1),normalize(request.getEmail()),passwords.encode(request.getPassword()),role.getValue());
        if(ids.isEmpty()) throw new ApiException(HttpStatus.CONFLICT,"EMAIL_ALREADY_EXISTS","Email already registered");
        var response=new UserResponse(); response.setId(ids.getFirst()); response.setEmail(normalize(request.getEmail())); response.setRole(role); return response;
    }
    @Transactional
    public TokenResponse login(LoginRequest request) {
        validatePasswordBytes(request.getPassword());
        var rows=jdbc.queryForList("SELECT id,password_hash,role FROM users WHERE email=?",normalize(request.getEmail()));
        String hash=rows.isEmpty()?dummyHash:(String)rows.getFirst().get("password_hash");
        if(!passwords.matches(request.getPassword(),hash) || rows.isEmpty()) throw new ApiException(HttpStatus.UNAUTHORIZED,"INVALID_CREDENTIALS","Incorrect email or password");
        var row=rows.getFirst(); return tokens.issue(((Number)row.get("id")).longValue(),(String)row.get("role"));
    }
    @Transactional
    public TokenResponse refresh(RefreshRequest request) {
        UUID jti; long userId;
        try {
            var claims=tokens.parse(request.getRefreshToken());
            if(!"refresh".equals(claims.get("token_type",String.class))) throw new IllegalArgumentException();
            jti=UUID.fromString(claims.getId()); userId=Long.parseLong(claims.getSubject());
        } catch(Exception e) { throw invalidRefresh(); }
        var rows=jdbc.queryForList("SELECT r.revoked,r.expires_at > CURRENT_TIMESTAMP AS valid,u.role FROM refresh_tokens r JOIN users u ON u.id=r.user_id WHERE r.jti=? AND r.user_id=? FOR UPDATE OF r",jti,userId);
        if(rows.isEmpty() || Boolean.TRUE.equals(rows.getFirst().get("revoked")) || !Boolean.TRUE.equals(rows.getFirst().get("valid"))) throw invalidRefresh();
        jdbc.update("UPDATE refresh_tokens SET revoked=TRUE WHERE jti=?",jti);
        return tokens.issue(userId,(String)rows.getFirst().get("role"));
    }
    private ApiException invalidRefresh() { return new ApiException(HttpStatus.UNAUTHORIZED,"REFRESH_TOKEN_INVALID","Invalid, expired or already used refresh token"); }
    private void validatePasswordBytes(String password) {
        if(password.getBytes(StandardCharsets.UTF_8).length>72)
            throw new ApiException(HttpStatus.BAD_REQUEST,"VALIDATION_ERROR","Password exceeds the bcrypt input limit",
                    java.util.Map.of("fields",java.util.List.of(java.util.Map.of("field","password","message","Must contain at most 72 UTF-8 bytes"))));
    }
    private String normalize(String email) { return email.toLowerCase(Locale.ROOT); }
}
