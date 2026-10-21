package ru.kozitskiy.marketplace.controller;

import org.springframework.core.io.ClassPathResource;
import org.springframework.core.io.Resource;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.RestController;

@RestController
public class ContractController {
    @GetMapping(value="/openapi.yaml",produces="application/yaml")
    public Resource openapi() { return new ClassPathResource("openapi/marketplace.yaml"); }
}
