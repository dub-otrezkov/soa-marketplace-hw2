package ru.kozitskiy.marketplace.logging;

import java.io.IOException;
import java.time.Instant;
import java.util.LinkedHashMap;
import java.util.Locale;
import java.util.Set;
import java.util.UUID;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;
import jakarta.servlet.FilterChain;
import jakarta.servlet.ServletException;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.servlet.http.HttpServletResponse;
import org.springframework.core.Ordered;
import org.springframework.core.annotation.Order;
import org.springframework.stereotype.Component;
import org.springframework.web.filter.OncePerRequestFilter;
import org.springframework.web.util.ContentCachingRequestWrapper;

@Component
@Order(Ordered.HIGHEST_PRECEDENCE)
public class ApiLoggingFilter extends OncePerRequestFilter {
    private static final int BODY_LIMIT=65536;
    private static final Set<String> SENSITIVE=Set.of("password","password_hash","access_token","refresh_token","authorization","secret","token");
    private final ObjectMapper mapper;
    public ApiLoggingFilter(ObjectMapper mapper) { this.mapper=mapper; }
    @Override protected void doFilterInternal(HttpServletRequest request,HttpServletResponse response,FilterChain chain) throws ServletException,IOException {
        var wrapped=new LoggedRequest(request);
        var id=UUID.randomUUID().toString(); var timestamp=Instant.now(); long start=System.nanoTime();
        response.setHeader("X-Request-Id",id);
        try { chain.doFilter(wrapped,response); }
        finally {
            var entry=new LinkedHashMap<String,Object>();
            entry.put("request_id",id); entry.put("method",request.getMethod()); entry.put("endpoint",request.getRequestURI());
            entry.put("status_code",response.getStatus()); entry.put("duration_ms",(System.nanoTime()-start)/1_000_000.0);
            entry.put("user_id",request.getAttribute("authenticated_user_id")); entry.put("timestamp",timestamp.toString());
            if(Set.of("POST","PUT","DELETE","PATCH").contains(request.getMethod())) {
                try {
                    // Security can reject the request before MVC consumes its body.
                    // Cache only a bounded amount; never print raw or truncated input.
                    if(wrapped.getContentAsByteArray().length==0 && request.getContentLengthLong()!=0)
                        wrapped.getInputStream().readNBytes(BODY_LIMIT+1);
                    byte[] body=wrapped.getContentAsByteArray();
                    if(wrapped.overflow) entry.put("request_body","[body exceeds log limit]");
                    else if(body.length==0) entry.put("request_body",null);
                    else { var json=mapper.readTree(body); redact(json); entry.put("request_body",json); }
                } catch(Exception e) { entry.put("request_body","[unparseable body omitted]"); }
            }
            System.out.println(mapper.writeValueAsString(entry));
        }
    }
    private static final class LoggedRequest extends ContentCachingRequestWrapper {
        private boolean overflow;
        private LoggedRequest(HttpServletRequest request) { super(request,BODY_LIMIT); }
        @Override protected void handleContentOverflow(int contentCacheLimit) { overflow=true; }
    }
    private void redact(JsonNode node) {
        if(node.isObject()) {
            var object=(ObjectNode)node;
            var names=new java.util.ArrayList<String>(); object.fieldNames().forEachRemaining(names::add);
            for(var name:names) {
                if(SENSITIVE.contains(name.toLowerCase(Locale.ROOT))) object.put(name,"***");
                else redact(object.get(name));
            }
        } else if(node.isArray()) node.forEach(this::redact);
    }
}
