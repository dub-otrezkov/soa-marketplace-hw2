package ru.kozitskiy.marketplace.security;

import java.security.Principal;
import java.util.Arrays;
import org.springframework.http.HttpStatus;
import org.springframework.security.core.context.SecurityContextHolder;
import ru.kozitskiy.marketplace.error.ApiException;

public final class AuthContext {
    private AuthContext() {}
    public record Actor(Long id, String role) implements Principal {
        public boolean isAdmin() { return "ADMIN".equals(role); }
        @Override public String getName() { return id.toString(); }
    }
    public static Actor current() {
        var auth=SecurityContextHolder.getContext().getAuthentication();
        if(auth==null || !(auth.getPrincipal() instanceof Actor actor)) throw new ApiException(HttpStatus.UNAUTHORIZED,"TOKEN_INVALID","Bearer access token is required");
        return actor;
    }
    public static Actor requireRoles(String... roles) {
        var actor=current();
        if(Arrays.stream(roles).noneMatch(actor.role()::equals)) throw new ApiException(HttpStatus.FORBIDDEN,"ACCESS_DENIED","Role does not allow this operation");
        return actor;
    }
}
