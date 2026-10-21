package ru.kozitskiy.marketplace.security;

import java.io.IOException;
import java.util.List;
import com.fasterxml.jackson.databind.ObjectMapper;
import io.jsonwebtoken.ExpiredJwtException;
import jakarta.servlet.FilterChain;
import jakarta.servlet.ServletException;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.servlet.http.HttpServletResponse;
import org.springframework.security.authentication.UsernamePasswordAuthenticationToken;
import org.springframework.security.core.authority.SimpleGrantedAuthority;
import org.springframework.security.core.context.SecurityContextHolder;
import org.springframework.web.filter.OncePerRequestFilter;
import ru.kozitskiy.marketplace.error.Errors;

public class JwtAuthenticationFilter extends OncePerRequestFilter {
    private final TokenService tokens;
    private final ObjectMapper mapper;
    public JwtAuthenticationFilter(TokenService tokens,ObjectMapper mapper) { this.tokens=tokens; this.mapper=mapper; }
    @Override protected boolean shouldNotFilter(HttpServletRequest request) { return request.getServletPath().startsWith("/auth/"); }
    @Override protected void doFilterInternal(HttpServletRequest request,HttpServletResponse response,FilterChain chain) throws ServletException,IOException {
        var header=request.getHeader("Authorization");
        if(header!=null) {
            try {
                if(!header.startsWith("Bearer ")) throw new IllegalArgumentException();
                var claims=tokens.parse(header.substring(7));
                if(!"access".equals(claims.get("token_type",String.class))) throw new IllegalArgumentException();
                var role=claims.get("role",String.class);
                if(!List.of("USER","SELLER","ADMIN").contains(role)) throw new IllegalArgumentException();
                var actor=new AuthContext.Actor(Long.parseLong(claims.getSubject()),role);
                SecurityContextHolder.getContext().setAuthentication(new UsernamePasswordAuthenticationToken(actor,null,List.of(new SimpleGrantedAuthority("ROLE_"+role))));
                request.setAttribute("authenticated_user_id",actor.id());
            } catch(ExpiredJwtException e) { reject(response,"TOKEN_EXPIRED","Access token expired"); return;
            } catch(Exception e) { reject(response,"TOKEN_INVALID","Invalid access token"); return; }
        }
        chain.doFilter(request,response);
    }
    private void reject(HttpServletResponse response,String code,String message) throws IOException {
        response.setStatus(401); response.setContentType("application/json");
        mapper.writeValue(response.getOutputStream(),Errors.body(code,message,null));
    }
}
