package ru.kozitskiy.marketplace.security;

import java.nio.charset.StandardCharsets;
import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.Date;
import java.util.UUID;
import javax.crypto.SecretKey;
import io.jsonwebtoken.Claims;
import io.jsonwebtoken.Jwts;
import io.jsonwebtoken.security.Keys;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.stereotype.Service;
import ru.kozitskiy.marketplace.api.model.TokenResponse;

@Service
public class TokenService {
    private final SecretKey key;
    private final JdbcTemplate jdbc;
    private final int accessMinutes;
    private final int refreshDays;
    public TokenService(JdbcTemplate jdbc, @Value("${marketplace.jwt-secret}") String secret,
            @Value("${marketplace.access-token-minutes}") int accessMinutes,
            @Value("${marketplace.refresh-token-days}") int refreshDays) {
        if (secret.getBytes(StandardCharsets.UTF_8).length < 32) throw new IllegalArgumentException("JWT_SECRET must contain at least 32 bytes");
        key=Keys.hmacShaKeyFor(secret.getBytes(StandardCharsets.UTF_8));
        this.jdbc=jdbc; this.accessMinutes=accessMinutes; this.refreshDays=refreshDays;
    }
    public Claims parse(String token) { return Jwts.parser().verifyWith(key).requireIssuer("soa-marketplace").build().parseSignedClaims(token).getPayload(); }
    public TokenResponse issue(long userId, String role) {
        var now=Instant.now();
        var refreshId=UUID.randomUUID();
        var expires=now.plus(refreshDays,ChronoUnit.DAYS);
        jdbc.update("INSERT INTO refresh_tokens(jti,user_id,expires_at) VALUES (?,?,?)",refreshId,userId,java.sql.Timestamp.from(expires));
        var response=new TokenResponse();
        response.setAccessToken(token(userId,role,"access",UUID.randomUUID(),now,now.plus(accessMinutes,ChronoUnit.MINUTES)));
        response.setRefreshToken(token(userId,role,"refresh",refreshId,now,expires));
        response.setTokenType("Bearer");
        response.setExpiresIn((long)accessMinutes*60);
        return response;
    }
    private String token(long userId,String role,String type,UUID id,Instant issued,Instant expiry) {
        return Jwts.builder().issuer("soa-marketplace").subject(Long.toString(userId)).id(id.toString())
            .claim("role",role).claim("token_type",type).issuedAt(Date.from(issued)).expiration(Date.from(expiry)).signWith(key).compact();
    }
}
