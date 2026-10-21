package ru.kozitskiy.marketplace.service;

import org.junit.jupiter.api.Test;
import org.springframework.http.HttpStatus;
import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.security.crypto.password.PasswordEncoder;
import ru.kozitskiy.marketplace.api.model.LoginRequest;
import ru.kozitskiy.marketplace.api.model.RegisterRequest;
import ru.kozitskiy.marketplace.api.model.Role;
import ru.kozitskiy.marketplace.error.ApiException;
import ru.kozitskiy.marketplace.security.TokenService;

import static org.assertj.core.api.Assertions.assertThatThrownBy;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.verifyNoInteractions;

class AuthServiceTest {
    @Test
    void multibytePasswordBeyondBcryptLimitIsAClientErrorOnRegistration() {
        var jdbc=mock(JdbcTemplate.class);
        var service=new AuthService(jdbc,mock(PasswordEncoder.class),mock(TokenService.class),"","");
        var request=new RegisterRequest().email("user@example.test").password("я".repeat(37)).role(Role.USER);
        assertThatThrownBy(()->service.register(request)).isInstanceOfSatisfying(ApiException.class,e->{
            org.assertj.core.api.Assertions.assertThat(e.status()).isEqualTo(HttpStatus.BAD_REQUEST);
            org.assertj.core.api.Assertions.assertThat(e.code()).isEqualTo("VALIDATION_ERROR");
        });
        verifyNoInteractions(jdbc);
    }

    @Test
    void multibytePasswordBeyondBcryptLimitIsAClientErrorOnLogin() {
        var jdbc=mock(JdbcTemplate.class);
        var service=new AuthService(jdbc,mock(PasswordEncoder.class),mock(TokenService.class),"","");
        var request=new LoginRequest().email("user@example.test").password("я".repeat(37));
        assertThatThrownBy(()->service.login(request)).isInstanceOfSatisfying(ApiException.class,e->
            org.assertj.core.api.Assertions.assertThat(e.status()).isEqualTo(HttpStatus.BAD_REQUEST));
        verifyNoInteractions(jdbc);
    }
}
