package ru.kozitskiy.marketplace.config;

import com.fasterxml.jackson.databind.cfg.CoercionAction;
import com.fasterxml.jackson.databind.cfg.CoercionInputShape;
import com.fasterxml.jackson.databind.type.LogicalType;
import org.springframework.boot.autoconfigure.jackson.Jackson2ObjectMapperBuilderCustomizer;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;

@Configuration
public class JacksonConfiguration {
    @Bean
    Jackson2ObjectMapperBuilderCustomizer strictTextValues() {
        // ALLOW_COERCION_OF_SCALARS does not disable number/boolean-to-String conversion.
        // The OpenAPI string schemas require an actual JSON string.
        return builder->builder.postConfigurer(mapper->mapper.coercionConfigFor(LogicalType.Textual)
                .setCoercion(CoercionInputShape.Integer,CoercionAction.Fail)
                .setCoercion(CoercionInputShape.Float,CoercionAction.Fail)
                .setCoercion(CoercionInputShape.Boolean,CoercionAction.Fail));
    }
}
