package ru.kozitskiy.marketplace.security;

import com.fasterxml.jackson.databind.ObjectMapper;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.security.config.annotation.web.builders.HttpSecurity;
import org.springframework.security.config.http.SessionCreationPolicy;
import org.springframework.security.crypto.bcrypt.BCryptPasswordEncoder;
import org.springframework.security.crypto.password.PasswordEncoder;
import org.springframework.security.web.SecurityFilterChain;
import org.springframework.security.web.authentication.UsernamePasswordAuthenticationFilter;
import ru.kozitskiy.marketplace.error.Errors;

@Configuration
public class SecurityConfiguration {
    @Bean PasswordEncoder passwordEncoder() { return new BCryptPasswordEncoder(); }
    @Bean SecurityFilterChain securityFilterChain(HttpSecurity http,TokenService tokens,ObjectMapper mapper) throws Exception {
        http.csrf(c -> c.disable()).sessionManagement(s -> s.sessionCreationPolicy(SessionCreationPolicy.STATELESS))
            .authorizeHttpRequests(a -> a.requestMatchers("/auth/register","/auth/login","/auth/refresh","/actuator/health","/openapi.yaml").permitAll().anyRequest().authenticated())
            .exceptionHandling(e -> e.authenticationEntryPoint((request,response,exception) -> {
                response.setStatus(401); response.setContentType("application/json");
                mapper.writeValue(response.getOutputStream(),Errors.body("TOKEN_INVALID","Bearer access token is required",null));
            }).accessDeniedHandler((request,response,exception) -> {
                response.setStatus(403); response.setContentType("application/json");
                mapper.writeValue(response.getOutputStream(),Errors.body("ACCESS_DENIED","Access denied",null));
            }))
            .addFilterBefore(new JwtAuthenticationFilter(tokens,mapper),UsernamePasswordAuthenticationFilter.class);
        return http.build();
    }
}
