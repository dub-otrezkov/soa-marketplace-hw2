package ru.kozitskiy.marketplace.controller;

import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.RestController;
import ru.kozitskiy.marketplace.api.ProductsApi;
import ru.kozitskiy.marketplace.api.model.*;
import ru.kozitskiy.marketplace.service.ProductService;

@RestController
public class ProductsController implements ProductsApi {
    private final ProductService service;
    public ProductsController(ProductService service) { this.service=service; }
    @Override public ResponseEntity<ProductResponse> createProduct(ProductCreate request) { var p=service.create(request); return ResponseEntity.created(java.net.URI.create("/products/"+p.getId())).body(p); }
    @Override public ResponseEntity<ProductResponse> getProduct(Long id) { return ResponseEntity.ok(service.get(id)); }
    @Override public ResponseEntity<ProductPage> listProducts(Integer page,Integer size,ProductStatus status,String category) { return ResponseEntity.ok(service.list(page,size,status,category)); }
    @Override public ResponseEntity<ProductResponse> updateProduct(Long id,ProductUpdate request) { return ResponseEntity.ok(service.update(id,request)); }
    @Override public ResponseEntity<Void> archiveProduct(Long id) { service.archive(id); return ResponseEntity.noContent().build(); }
}
