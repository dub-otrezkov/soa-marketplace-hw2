package ru.kozitskiy.marketplace.controller;

import org.springframework.http.HttpStatus;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.RestController;
import ru.kozitskiy.marketplace.api.OrdersApi;
import ru.kozitskiy.marketplace.api.model.OrderCreate;
import ru.kozitskiy.marketplace.api.model.OrderResponse;
import ru.kozitskiy.marketplace.api.model.OrderStatusUpdate;
import ru.kozitskiy.marketplace.api.model.OrderUpdate;
import ru.kozitskiy.marketplace.service.OrderService;

@RestController
public class OrdersController implements OrdersApi {
    private final OrderService orders;

    public OrdersController(OrderService orders) {
        this.orders = orders;
    }

    @Override
    public ResponseEntity<OrderResponse> createOrder(OrderCreate request) {
        return ResponseEntity.status(HttpStatus.CREATED).body(orders.create(request));
    }

    @Override
    public ResponseEntity<OrderResponse> getOrder(Long id) {
        return ResponseEntity.ok(orders.get(id));
    }

    @Override
    public ResponseEntity<OrderResponse> updateOrder(Long id, OrderUpdate request) {
        return ResponseEntity.ok(orders.update(id, request));
    }

    @Override
    public ResponseEntity<OrderResponse> cancelOrder(Long id) {
        return ResponseEntity.ok(orders.cancel(id));
    }

    @Override
    public ResponseEntity<OrderResponse> transitionOrder(Long id, OrderStatusUpdate request) {
        return ResponseEntity.ok(orders.transition(id, request));
    }
}
