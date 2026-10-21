package ru.kozitskiy.marketplace.controller;

import org.springframework.http.HttpStatus;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.RestController;
import ru.kozitskiy.marketplace.api.PromoCodesApi;
import ru.kozitskiy.marketplace.api.model.PromoCodeCreate;
import ru.kozitskiy.marketplace.api.model.PromoCodeResponse;
import ru.kozitskiy.marketplace.service.PromoCodeService;

@RestController
public class PromoCodesController implements PromoCodesApi {
    private final PromoCodeService promos;

    public PromoCodesController(PromoCodeService promos) {
        this.promos = promos;
    }

    @Override
    public ResponseEntity<PromoCodeResponse> createPromoCode(PromoCodeCreate request) {
        return ResponseEntity.status(HttpStatus.CREATED).body(promos.create(request));
    }
}
