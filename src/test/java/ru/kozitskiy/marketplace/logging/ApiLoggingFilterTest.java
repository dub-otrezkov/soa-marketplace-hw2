package ru.kozitskiy.marketplace.logging;

import java.nio.charset.StandardCharsets;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.springframework.boot.test.system.CapturedOutput;
import org.springframework.boot.test.system.OutputCaptureExtension;
import org.springframework.mock.web.MockHttpServletRequest;
import org.springframework.mock.web.MockHttpServletResponse;

import static org.assertj.core.api.Assertions.assertThat;

@ExtendWith(OutputCaptureExtension.class)
class ApiLoggingFilterTest {
    private final ObjectMapper mapper=new ObjectMapper();

    @Test
    void logsAndRedactsBodyEvenWhenSecurityRejectsBeforeReadingIt(CapturedOutput output) throws Exception {
        var response=reject("{\"email\":\"user@example.test\",\"password\":\"private-password\",\"nested\":{\"refresh_token\":\"private-token\"}}");
        JsonNode log=lastLog(output);
        assertThat(log.path("status_code").asInt()).isEqualTo(401);
        assertThat(log.path("request_body").path("email").asText()).isEqualTo("user@example.test");
        assertThat(log.path("request_body").path("password").asText()).isEqualTo("***");
        assertThat(log.path("request_body").path("nested").path("refresh_token").asText()).isEqualTo("***");
        assertThat(log.path("request_id").asText()).isEqualTo(response.getHeader("X-Request-Id"));
        assertThat(output.getOut()).doesNotContain("private-password","private-token");
    }

    @Test
    void omitsOversizedBodyWithoutLoggingItsTruncatedContent(CapturedOutput output) throws Exception {
        reject("{\"password\":\""+"private-secret".repeat(6000)+"\"}");
        assertThat(lastLog(output).path("request_body").asText()).isEqualTo("[body exceeds log limit]");
        assertThat(output.getOut()).doesNotContain("private-secret");
    }

    @Test
    void malformedJsonNeverLeaksUnredactedPasswords(CapturedOutput output) throws Exception {
        reject("{\"password\":\"private-secret\"");
        assertThat(lastLog(output).path("request_body").asText()).isEqualTo("[unparseable body omitted]");
        assertThat(output.getOut()).doesNotContain("private-secret");
    }

    private MockHttpServletResponse reject(String body) throws Exception {
        var request=new MockHttpServletRequest("POST","/products");
        request.setContentType("application/json");
        request.setContent(body.getBytes(StandardCharsets.UTF_8));
        var response=new MockHttpServletResponse();
        new ApiLoggingFilter(mapper).doFilter(request,response,(req,res)->((jakarta.servlet.http.HttpServletResponse)res).setStatus(401));
        return response;
    }
    private JsonNode lastLog(CapturedOutput output) throws Exception {
        String line=output.getOut().lines().filter(s->s.startsWith("{\"request_id\"")).reduce((first,last)->last).orElseThrow();
        return mapper.readTree(line);
    }
}
