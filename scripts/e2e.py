#!/usr/bin/env python3
"""Black-box marketplace checks. Uses only Python's standard library.

Every run creates uniquely named users/products/promos. --db-checks also executes
SELECTs and ages CREATE_ORDER/UPDATE_ORDER timestamps ONLY for those test users.
Run against a disposable development database; existing data is never deleted.
"""

import argparse
import base64
import concurrent.futures
import hashlib
import hmac
import json
import os
from pathlib import Path
import subprocess
import sys
import threading
import time
import unittest
import urllib.error
import urllib.parse
import urllib.request
import uuid
from datetime import datetime, timedelta, timezone
from decimal import Decimal


ROOT = Path(__file__).resolve().parents[1]
RUN = uuid.uuid4().hex[:10]
PASSWORD = "E2ePassword123!"
MISSING_ID = 9223372036854775807
OPTIONS = None


class Client:
    def __init__(self, token=None):
        self.token = token

    def request(self, method, path, body=None, expected=200, error=None):
        headers = {"Accept": "application/json"}
        if self.token:
            headers["Authorization"] = "Bearer " + self.token
        payload = None
        if body is not None:
            headers["Content-Type"] = "application/json"
            payload = json.dumps(body).encode()
        request = urllib.request.Request(OPTIONS.base_url.rstrip("/") + path,
                                         payload, headers, method=method)
        try:
            response = urllib.request.urlopen(request, timeout=30)
        except urllib.error.HTTPError as exc:
            response = exc
        raw = response.read().decode()
        try:
            data = json.loads(raw) if raw else None
        except json.JSONDecodeError as exc:
            raise AssertionError(f"{method} {path}: non-JSON response {raw[:300]}") from exc
        request_id = response.headers.get("X-Request-Id")
        try:
            uuid.UUID(request_id or "")
        except ValueError as exc:
            raise AssertionError(f"{method} {path}: missing/invalid X-Request-Id") from exc
        allowed = expected if isinstance(expected, tuple) else (expected,)
        assert response.status in allowed, (method, path, response.status, allowed, data)
        if response.status >= 400:
            assert isinstance(data, dict), data
            assert isinstance(data.get("error_code"), str) and data["error_code"], data
            assert isinstance(data.get("message"), str) and data["message"], data
            assert data.get("details") is None or isinstance(data["details"], dict), data
            if error:
                assert data["error_code"] == error, data
        elif error:
            raise AssertionError(f"Expected error {error}, got {data}")
        return data


class MarketplaceE2E(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.anonymous = Client()
        cls.registered_users = set()
        cls.users_lock = threading.Lock()
        cls.admin = Client(cls.anonymous.request("POST", "/auth/login", {
            "email": OPTIONS.admin_email, "password": OPTIONS.admin_password,
        })["access_token"])
        cls.seller, cls.seller_user, _ = cls.account("SELLER")
        cls.other_seller, _, _ = cls.account("SELLER")

    @classmethod
    def account(cls, role="USER"):
        email = f"e2e-{RUN}-{uuid.uuid4().hex[:12]}@example.test"
        user = cls.anonymous.request("POST", "/auth/register", {
            "email": email, "password": PASSWORD, "role": role,
        }, expected=201)
        with cls.users_lock:
            cls.registered_users.add(user["id"])
        tokens = cls.anonymous.request("POST", "/auth/login", {
            "email": email, "password": PASSWORD,
        })
        assert tokens["access_token"] != tokens["refresh_token"], tokens
        return Client(tokens["access_token"]), user, tokens

    @staticmethod
    def money(actual, expected):
        assert Decimal(str(actual)) == Decimal(str(expected)), (actual, expected)

    def product(self, **overrides):
        body = {"name": f"E2E {RUN}", "description": None, "price": 100,
                "stock": 20, "category": f"E2E_{RUN}", "status": "ACTIVE"}
        body.update(overrides)
        return self.seller.request("POST", "/products", body, expected=201)

    def change_product(self, product, **overrides):
        current = self.seller.request("GET", f"/products/{product['id']}")
        body = {key: current.get(key) for key in
                ("name", "description", "price", "stock", "category", "status")}
        body.update(overrides)
        return self.seller.request("PUT", f"/products/{product['id']}", body)

    def order(self, client, items, promo=None, **kwargs):
        body = {"items": [{"product_id": p["id"], "quantity": q} for p, q in items]}
        if promo is not None:
            body["promo_code"] = promo
        return client.request("POST", "/orders", body, expected=kwargs.pop("expected", 201), **kwargs)

    @staticmethod
    def update(client, order, items, **kwargs):
        return client.request("PUT", f"/orders/{order['id']}", {
            "items": [{"product_id": p["id"], "quantity": q} for p, q in items],
        }, **kwargs)

    @staticmethod
    def cancel(client, order, **kwargs):
        return client.request("POST", f"/orders/{order['id']}/cancel", **kwargs)

    def promo(self, **overrides):
        now = datetime.now(timezone.utc)
        body = {"code": "E" + uuid.uuid4().hex[:15].upper(), "discount_type": "PERCENTAGE",
                "discount_value": 20, "min_order_amount": 0, "max_uses": 10,
                "valid_from": (now - timedelta(days=1)).isoformat(),
                "valid_until": (now + timedelta(days=1)).isoformat(), "active": True}
        body.update(overrides)
        return self.seller.request("POST", "/promo-codes", body, expected=201)

    def stock(self, product):
        return self.seller.request("GET", f"/products/{product['id']}")["stock"]

    @staticmethod
    def sql(query):
        if not OPTIONS.db_checks:
            raise unittest.SkipTest("requires --db-checks (isolated development database)")
        result = subprocess.run(["docker", "compose", "exec", "-T", "db", "psql",
                                 "-X", "-U", "marketplace", "-d", "marketplace",
                                 "-v", "ON_ERROR_STOP=1", "-At", "-c", query],
                                cwd=ROOT, check=True, capture_output=True, text=True)
        return result.stdout.strip()

    def age_operations(self, user):
        self.assertIn(user["id"], self.registered_users)
        user_id = int(user["id"])
        self.sql(f"UPDATE user_operations SET created_at = CURRENT_TIMESTAMP - INTERVAL '365 days' "
                 f"WHERE user_id = '{user_id}';")

    def uses(self, promo):
        promo_id = int(promo["id"])
        return int(self.sql(f"SELECT current_uses FROM promo_codes WHERE id = '{promo_id}';"))

    def test_01_authentication_and_token_types(self):
        client, user, tokens = self.account()
        self.assertEqual(user["role"], "USER")
        self.anonymous.request("GET", "/products", expected=401, error="TOKEN_INVALID")
        Client("broken.jwt.token").request("GET", "/products", expected=401, error="TOKEN_INVALID")
        Client(tokens["refresh_token"]).request("GET", "/products", expected=401, error="TOKEN_INVALID")
        for token in ("broken", tokens["access_token"]):
            self.anonymous.request("POST", "/auth/refresh", {"refresh_token": token},
                                   expected=401, error="REFRESH_TOKEN_INVALID")
        refreshed = self.anonymous.request("POST", "/auth/refresh", {
            "refresh_token": tokens["refresh_token"],
        })
        self.assertNotEqual(refreshed["refresh_token"], tokens["refresh_token"])
        self.anonymous.request("POST", "/auth/refresh", {"refresh_token": tokens["refresh_token"]},
                               expected=401, error="REFRESH_TOKEN_INVALID")
        Client(refreshed["access_token"]).request("GET", "/products")
        for kind, lifetime in (("access", 20 * 60), ("refresh", 14 * 24 * 60 * 60)):
            encoded = tokens[kind + "_token"].split(".")[1]
            claims = json.loads(base64.urlsafe_b64decode(encoded + "=" * (-len(encoded) % 4)))
            self.assertEqual(claims["token_type"], kind)
            self.assertEqual(claims["role"], "USER")
            self.assertEqual(claims["exp"] - claims["iat"], lifetime)
        self.anonymous.request("POST", "/auth/login", {"email": user["email"], "password": "wrong"},
                               expected=401)
        self.anonymous.request("POST", "/auth/register", {
            "email": f"e2e-{RUN}-admin@example.test", "password": PASSWORD, "role": "ADMIN",
        }, expected=400, error="VALIDATION_ERROR")
        client.request("GET", "/products")

    def test_02_product_crud_filters_and_soft_delete(self):
        category = "FILTER_" + uuid.uuid4().hex[:8]
        first = self.product(category=category, price=12.34)
        second = self.product(category=category, status="INACTIVE")
        self.assertTrue(first["created_at"] and first["updated_at"])
        page = self.seller.request("GET", "/products?" + urllib.parse.urlencode({"category": category, "size": 1}))
        self.assertEqual((page["totalElements"], page["page"], page["size"], len(page["items"])), (2, 0, 1, 1))
        next_page = self.seller.request("GET", f"/products?category={category}&page=1&size=1")
        self.assertNotEqual(page["items"][0]["id"], next_page["items"][0]["id"])
        filtered = self.seller.request("GET", f"/products?category={category}&status=INACTIVE")
        self.assertEqual(filtered["totalElements"], 1)
        self.assertEqual(filtered["size"], 20)
        self.assertEqual(filtered["items"][0]["id"], second["id"])
        self.assertEqual(self.seller.request("GET", f"/products?category={category}X")["totalElements"], 0)
        changed = self.change_product(first, name="Updated", price=42.25, stock=9)
        self.assertEqual(changed["name"], "Updated")
        self.money(changed["price"], "42.25")
        self.seller.request("DELETE", f"/products/{first['id']}", expected=204)
        self.assertEqual(self.seller.request("GET", f"/products/{first['id']}")["status"], "ARCHIVED")
        if OPTIONS.db_checks:
            self.assertEqual(self.sql(f"SELECT status FROM products WHERE id='{first['id']}';"), "ARCHIVED")
        self.seller.request("GET", f"/products/{MISSING_ID}", expected=404, error="PRODUCT_NOT_FOUND")

    def test_03_validation_boundaries(self):
        valid = {"name": "ok", "description": None, "price": 1,
                 "stock": 0, "category": "ok", "status": "ACTIVE"}
        for field, value in [("name", ""), ("name", "x" * 256), ("description", "x" * 4001),
                             ("price", 0), ("price", -1), ("stock", -1), ("category", ""),
                             ("category", "x" * 101), ("status", "UNKNOWN")]:
            with self.subTest(field=field, value=str(value)[:30]):
                error = self.seller.request("POST", "/products", dict(valid, **{field: value}),
                                            expected=400, error="VALIDATION_ERROR")
                self.assertTrue(error.get("details"), error)
        for query in ("page=-1", "size=0", "status=UNKNOWN"):
            self.seller.request("GET", "/products?" + query, expected=400, error="VALIDATION_ERROR")
        self.seller.request("GET", "/products/not-a-uuid", expected=400, error="VALIDATION_ERROR")
        user, _, _ = self.account()
        p = self.product()
        for body in ({"items": []}, {"items": [{"product_id": p["id"], "quantity": 1}] * 51},
                     {"items": [{"product_id": p["id"], "quantity": 0}]},
                     {"items": [{"product_id": p["id"], "quantity": 1000}]},
                     {"items": [{"product_id": p["id"], "quantity": 1}], "promo_code": "bad"}):
            error = user.request("POST", "/orders", body, expected=400, error="VALIDATION_ERROR")
            self.assertTrue(error.get("details"), error)
        self.assertEqual(self.stock(p), 20)
        user.request("POST", "/orders", {"items": [{"product_id": p["id"], "quantity": 600}] * 2},
                     expected=400, error="VALIDATION_ERROR")
        for invalid_body in ({"items": [{"product_id": p["id"], "quantity": "1"}]},
                             {"items": [{"product_id": p["id"], "quantity": 1}], "user_id": 1}):
            user.request("POST", "/orders", invalid_body, expected=400, error="VALIDATION_ERROR")

    def test_04_roles_and_ownership(self):
        user, _, _ = self.account()
        other, _, _ = self.account()
        p = self.product()
        body = {k: p[k] for k in ("name", "description", "price", "stock", "category", "status")}
        for method, path, payload in [("POST", "/products", body), ("PUT", f"/products/{p['id']}", body),
                                      ("DELETE", f"/products/{p['id']}", None)]:
            user.request(method, path, payload, expected=403, error="ACCESS_DENIED")
        self.other_seller.request("PUT", f"/products/{p['id']}", body, expected=403, error="ACCESS_DENIED")
        self.other_seller.request("DELETE", f"/products/{p['id']}", expected=403, error="ACCESS_DENIED")
        self.other_seller.request("POST", "/products", dict(body, seller_id=self.seller_user["id"]),
                                  expected=403, error="ACCESS_DENIED")
        self.admin.request("PUT", f"/products/{p['id']}", body)
        admin_product = self.admin.request("POST", "/products", dict(body, seller_id=self.seller_user["id"]), expected=201)
        self.assertEqual(admin_product["seller_id"], self.seller_user["id"])
        self.admin.request("DELETE", f"/products/{admin_product['id']}", expected=204)
        self.order(self.seller, [(p, 1)], expected=403, error="ACCESS_DENIED")
        o = self.order(user, [(p, 1)])
        for method, suffix, payload in [("GET", "", None), ("PUT", "", {"items": [{"product_id": p["id"], "quantity": 1}]}),
                                        ("POST", "/cancel", None)]:
            other.request(method, f"/orders/{o['id']}{suffix}", payload,
                          expected=403, error="ORDER_OWNERSHIP_VIOLATION")
            self.seller.request(method, f"/orders/{o['id']}{suffix}", payload,
                                expected=403, error="ACCESS_DENIED")
        user.request("PATCH", f"/orders/{o['id']}/status", {"status": "PAYMENT_PENDING"},
                     expected=403, error="ACCESS_DENIED")
        self.assertEqual(self.admin.request("GET", f"/orders/{o['id']}")["id"], o["id"])
        self.cancel(self.admin, o)
        self.assertEqual(self.stock(p), 20)
        user.request("GET", f"/orders/{MISSING_ID}", expected=404, error="ORDER_NOT_FOUND")
        promo_body = {"code": "DENIED" + RUN[:6].upper(), "discount_type": "PERCENTAGE", "discount_value": 10,
                      "min_order_amount": 0, "max_uses": 1,
                      "valid_from": "2020-01-01T00:00:00Z", "valid_until": "2099-01-01T00:00:00Z", "active": True}
        user.request("POST", "/promo-codes", promo_body, expected=403, error="ACCESS_DENIED")

    def test_05_snapshot_duplicates_update_and_cooldown(self):
        user, _, _ = self.account()
        p = self.product(price=12.34, stock=10)
        o = self.order(user, [(p, 1), (p, 2)])
        self.assertEqual(len(o["items"]), 1)
        self.assertEqual(o["items"][0]["quantity"], 3)
        self.money(o["items"][0]["price_at_order"], "12.34")
        self.money(o["total_amount"], "37.02")
        self.assertEqual(self.stock(p), 7)
        self.change_product(p, price=99)
        self.money(user.request("GET", f"/orders/{o['id']}")["total_amount"], "37.02")
        updated = self.update(user, o, [(p, 4)])
        self.money(updated["total_amount"], "49.36")
        self.assertEqual(self.stock(p), 6)
        self.update(user, o, [(p, 1)], expected=429, error="ORDER_LIMIT_EXCEEDED")
        self.order(user, [(p, 1)], expected=429, error="ORDER_LIMIT_EXCEEDED")
        if OPTIONS.db_checks:
            self.assertEqual(self.sql(f"SELECT quantity || ':' || price_at_order FROM order_items WHERE order_id='{o['id']}';"), "4:12.34")
        self.cancel(user, o)
        self.assertEqual(self.stock(p), 10)
        self.update(user, o, [(p, 1)], expected=409, error="INVALID_STATE_TRANSITION")
        self.cancel(user, o, expected=409, error="INVALID_STATE_TRANSITION")

    def test_06_catalog_before_stock_and_atomic_create(self):
        user, info, _ = self.account()
        plenty = self.product(stock=5)
        empty = self.product(stock=0)
        inactive = self.product(status="INACTIVE")
        self.order(user, [(empty, 1), ({"id": MISSING_ID}, 1)], expected=404, error="PRODUCT_NOT_FOUND")
        self.order(user, [(empty, 1), (inactive, 1)], expected=409, error="PRODUCT_INACTIVE")
        error = self.order(user, [(plenty, 2), (empty, 1)], expected=409, error="INSUFFICIENT_STOCK")
        self.assertTrue(error.get("details"))
        self.assertEqual(self.stock(plenty), 5)
        self.assertEqual(self.stock(empty), 0)
        if OPTIONS.db_checks:
            self.assertEqual(self.sql(f"SELECT count(*) FROM orders WHERE user_id='{info['id']}';"), "0")
            self.assertEqual(self.sql(f"SELECT count(*) FROM user_operations WHERE user_id='{info['id']}';"), "0")
        self.order(user, [(plenty, 1)])  # Failed attempts did not consume cooldown.

    def test_07_update_failure_restores_previous_order(self):
        user, _, _ = self.account()
        old, new, empty = self.product(stock=5), self.product(stock=5), self.product(stock=0)
        o = self.order(user, [(old, 3)])
        self.update(user, o, [(new, 2), (empty, 1)], expected=409, error="INSUFFICIENT_STOCK")
        self.assertEqual([self.stock(p) for p in (old, new, empty)], [2, 5, 0])
        unchanged = user.request("GET", f"/orders/{o['id']}")
        self.assertEqual(unchanged["items"], o["items"])
        self.money(unchanged["total_amount"], 300)
        updated = self.update(user, o, [(new, 2)])
        self.money(updated["total_amount"], 200)
        self.assertEqual([self.stock(p) for p in (old, new)], [5, 3])

    def test_08_promo_discount_math_and_cancel(self):
        for kwargs, expected_discount, expected_total in [
            ({"discount_value": 20}, 40, 160),
            ({"discount_value": 70}, 140, 60),
            ({"discount_value": 71}, 0, 200),
            ({"discount_type": "FIXED_AMOUNT", "discount_value": 500}, 200, 0),
        ]:
            with self.subTest(promo=kwargs):
                user, _, _ = self.account()
                p = self.product(price=100, stock=5)
                promo = self.promo(**kwargs)
                o = self.order(user, [(p, 2)], promo["code"])
                self.money(o["discount_amount"], expected_discount)
                self.money(o["total_amount"], expected_total)
                if OPTIONS.db_checks:
                    self.assertEqual(self.uses(promo), 1)
                self.cancel(user, o)
                self.assertEqual(self.stock(p), 5)
                if OPTIONS.db_checks:
                    self.assertEqual(self.uses(promo), 0)

    def test_09_invalid_promos_and_minimum_rollback(self):
        user, _, _ = self.account()
        p = self.product(stock=10)
        now = datetime.now(timezone.utc)
        invalid = ["MISSING" + RUN[:8].upper(), self.promo(active=False)["code"],
                   self.promo(valid_from=(now - timedelta(days=3)).isoformat(),
                              valid_until=(now - timedelta(days=2)).isoformat())["code"],
                   self.promo(valid_from=(now + timedelta(days=1)).isoformat(),
                              valid_until=(now + timedelta(days=2)).isoformat())["code"]]
        limited = self.promo(max_uses=1)
        consumer, _, _ = self.account()
        consumed = self.order(consumer, [(p, 1)], limited["code"])
        invalid.append(limited["code"])
        for code in invalid:
            self.order(user, [(p, 1)], code, expected=422, error="PROMO_CODE_INVALID")
            self.assertEqual(self.stock(p), 9)
        minimum = self.promo(min_order_amount=200)
        self.order(user, [(p, 1)], minimum["code"], expected=422, error="PROMO_CODE_MIN_AMOUNT")
        self.assertEqual(self.stock(p), 9)
        if OPTIONS.db_checks:
            self.assertEqual(self.uses(minimum), 0)
        self.cancel(consumer, consumed)
        self.order(user, [(p, 1)], limited["code"])  # Cancellation frees the promo slot.

    def test_10_update_retains_single_use_promo(self):
        user, _, _ = self.account()
        p = self.product(price=100, stock=10)
        promo = self.promo(max_uses=1, min_order_amount=100)
        o = self.order(user, [(p, 2)], promo["code"])
        updated = self.update(user, o, [(p, 3)])
        self.money(updated["discount_amount"], 60)
        self.money(updated["total_amount"], 240)
        if OPTIONS.db_checks:
            self.assertEqual(self.uses(promo), 1)
        self.cancel(user, o)
        if OPTIONS.db_checks:
            self.assertEqual(self.uses(promo), 0)

    def test_11_update_removes_promo_below_minimum(self):
        user, _, _ = self.account()
        p = self.product(price=100)
        promo = self.promo(min_order_amount=200, max_uses=1)
        o = self.order(user, [(p, 3)], promo["code"])
        updated = self.update(user, o, [(p, 1)])
        self.money(updated["discount_amount"], 0)
        self.money(updated["total_amount"], 100)
        self.assertIsNone(updated.get("promo_code_id"))
        if OPTIONS.db_checks:
            self.assertEqual(self.uses(promo), 0)

    def test_12_state_machine_and_admin_access(self):
        user, _, _ = self.account()
        p = self.product(stock=5)
        o = self.order(user, [(p, 2)])
        path = f"/orders/{o['id']}/status"
        self.admin.request("PATCH", path, {"status": "SHIPPED"}, expected=409, error="INVALID_STATE_TRANSITION")
        for state in ("PAYMENT_PENDING", "PAID", "SHIPPED", "COMPLETED"):
            changed = self.admin.request("PATCH", path, {"status": state})
            self.assertEqual(changed["status"], state)
            self.update(user, o, [(p, 1)], expected=409, error="INVALID_STATE_TRANSITION")
            if state != "PAYMENT_PENDING":
                self.cancel(user, o, expected=409, error="INVALID_STATE_TRANSITION")
        self.assertEqual(self.stock(p), 3)
        self.admin.request("PATCH", path, {"status": "CREATED"}, expected=409, error="INVALID_STATE_TRANSITION")
        another, _, _ = self.account()
        pending = self.order(another, [(p, 1)])
        self.admin.request("PATCH", f"/orders/{pending['id']}/status", {"status": "PAYMENT_PENDING"})
        self.assertEqual(self.cancel(another, pending)["status"], "CANCELED")
        self.assertEqual(self.stock(p), 3)

    def test_13_active_order_and_error_ordering_with_db(self):
        if not OPTIONS.db_checks:
            self.skipTest("--db-checks ages only this test user's cooldown timestamps")
        user, info, _ = self.account()
        p = self.product()
        self.order(user, [(p, 1)])
        missing = {"id": MISSING_ID}
        self.order(user, [(missing, 1)], expected=429, error="ORDER_LIMIT_EXCEEDED")
        self.age_operations(info)
        self.order(user, [(missing, 1)], expected=409, error="ORDER_HAS_ACTIVE")
        self.assertEqual(self.sql(f"SELECT count(*) FROM orders WHERE user_id='{info['id']}';"), "1")

    @staticmethod
    def race(callables):
        barrier = threading.Barrier(len(callables))
        def go(fn):
            barrier.wait(timeout=15)
            return fn()
        with concurrent.futures.ThreadPoolExecutor(max_workers=len(callables)) as pool:
            return list(pool.map(go, callables))

    def test_14_concurrent_stock_reservation(self):
        clients = [self.account()[0] for _ in range(5)]
        p = self.product(stock=1)
        outcomes = self.race([lambda c=c: self.order(c, [(p, 1)], expected=(201, 409)) for c in clients])
        winners = [o for o in outcomes if "id" in o]
        losers = [o for o in outcomes if "error_code" in o]
        self.assertEqual(len(winners), 1, outcomes)
        self.assertEqual({o["error_code"] for o in losers}, {"INSUFFICIENT_STOCK"})
        self.assertEqual(self.stock(p), 0)

    def test_15_concurrent_single_active_order(self):
        user, info, _ = self.account()
        p = self.product(stock=10)
        outcomes = self.race([lambda: self.order(user, [(p, 1)], expected=(201, 429)) for _ in range(5)])
        self.assertEqual(sum("id" in o for o in outcomes), 1, outcomes)
        self.assertEqual({o["error_code"] for o in outcomes if "error_code" in o}, {"ORDER_LIMIT_EXCEEDED"})
        self.assertEqual(self.stock(p), 9)
        if OPTIONS.db_checks:
            self.assertEqual(self.sql(f"SELECT count(*) FROM orders WHERE user_id='{info['id']}' AND status IN ('CREATED','PAYMENT_PENDING');"), "1")

    def test_16_concurrent_promo_cap(self):
        clients = [self.account()[0] for _ in range(5)]
        # Distinct products ensure that the contention under test is the promo lock.
        products = [self.product(stock=2) for _ in clients]
        promo = self.promo(max_uses=2)
        outcomes = self.race([lambda c=c, p=p: self.order(c, [(p, 1)], promo["code"], expected=(201, 422))
                              for c, p in zip(clients, products)])
        self.assertEqual(sum("id" in o for o in outcomes), 2, outcomes)
        self.assertEqual({o["error_code"] for o in outcomes if "error_code" in o}, {"PROMO_CODE_INVALID"})
        self.assertEqual(sum(self.stock(p) for p in products), 8)
        if OPTIONS.db_checks:
            self.assertEqual(self.uses(promo), 2)

    def test_17_inactive_promo_update_rolls_back_with_db(self):
        if not OPTIONS.db_checks:
            self.skipTest("--db-checks changes only a newly created test promo")
        user, _, _ = self.account()
        p = self.product(stock=10)
        promo = self.promo()
        o = self.order(user, [(p, 2)], promo["code"])
        self.sql(f"UPDATE promo_codes SET active=false WHERE id={int(promo['id'])};")
        self.update(user, o, [(p, 3)], expected=422, error="PROMO_CODE_INVALID")
        self.assertEqual(self.stock(p), 8)
        self.assertEqual(self.uses(promo), 1)
        unchanged = user.request("GET", f"/orders/{o['id']}")
        self.assertEqual(unchanged["items"], o["items"])
        self.money(unchanged["total_amount"], 160)
        self.cancel(user, o)
        self.assertEqual(self.uses(promo), 0)

    def test_18_refresh_rotation_race(self):
        _, user, tokens = self.account()
        outcomes = self.race([lambda: self.anonymous.request("POST", "/auth/refresh", {
            "refresh_token": tokens["refresh_token"],
        }, expected=(200, 401)) for _ in range(4)])
        self.assertEqual(sum("access_token" in result for result in outcomes), 1)
        self.assertEqual({result["error_code"] for result in outcomes if "error_code" in result},
                         {"REFRESH_TOKEN_INVALID"})
        if OPTIONS.db_checks:
            self.assertEqual(self.sql(f"SELECT count(*) FROM refresh_tokens WHERE user_id={int(user['id'])} AND NOT revoked;"), "1")
            self.assertEqual(self.sql(f"SELECT count(*) FROM refresh_tokens WHERE user_id={int(user['id'])} AND revoked;"), "1")

    def assert_validation_fields(self, response, field_fragment):
        fields = response.get("details", {}).get("fields")
        self.assertIsInstance(fields, list, response)
        self.assertTrue(fields, response)
        self.assertTrue(all(isinstance(item, dict) and item.get("field") and item.get("message")
                            for item in fields), response)
        # Accept both JSON field names and property paths from contract validators;
        # the contract requires a concrete field, without mandating its casing.
        wanted = field_fragment.replace("_", "").lower()
        self.assertTrue(any(wanted in item["field"].replace("_", "").lower() for item in fields), response)

    def test_19_strict_contract_regressions(self):
        user, _, _ = self.account()
        p = self.product(stock=10)
        valid_product = {k: p[k] for k in ("name", "description", "price", "stock", "category", "status")}
        for patch, field in (({"stock": 2.5}, "stock"), ({"price": 1.001}, "price"),
                             ({"unknown_field": True}, "unknown_field"), ({"price": {}}, "price"),
                             ({"name": 123}, "name")):
            for method, path in (("POST", "/products"), ("PUT", f"/products/{p['id']}")):
                with self.subTest(method=method, field=field):
                    error = self.seller.request(method, path, dict(valid_product, **patch),
                                                expected=400, error="VALIDATION_ERROR")
                    self.assert_validation_fields(error, field)
        self.assertEqual(self.stock(p), 10)
        invalid_orders = [({"items": [None]}, "items"),
                          ({"items": [{"product_id": p["id"], "quantity": 1.5}]}, "quantity"),
                          ({"items": [{"product_id": p["id"], "quantity": "1"}]}, "quantity"),
                          ({"items": [{"product_id": p["id"], "quantity": 1, "extra": 1}]}, "extra"),
                          ({"items": [{"product_id": p["id"], "quantity": 1}], "user_id": 1}, "user_id")]
        for body, field in invalid_orders:
            with self.subTest(operation="create", field=field):
                error = user.request("POST", "/orders", body, expected=400, error="VALIDATION_ERROR")
                self.assert_validation_fields(error, field)
        o = self.order(user, [(p, 2)])
        for body, field in invalid_orders:
            with self.subTest(operation="update", field=field):
                error = user.request("PUT", f"/orders/{o['id']}", body, expected=400, error="VALIDATION_ERROR")
                self.assert_validation_fields(error, field)
        self.assertEqual(self.stock(p), 8)
        self.assertEqual(user.request("GET", f"/orders/{o['id']}")["items"], o["items"])
        valid_promo = {"code": "RG" + uuid.uuid4().hex[:12].upper(), "discount_type": "PERCENTAGE",
                       "discount_value": 10, "min_order_amount": 0, "max_uses": 1,
                       "valid_from": "2020-01-01T00:00:00Z", "valid_until": "2099-01-01T00:00:00Z", "active": True}
        for field in ("discount_value", "min_order_amount"):
            error = self.seller.request("POST", "/promo-codes", dict(valid_promo, **{field: 1.001}),
                                        expected=400, error="VALIDATION_ERROR")
            self.assert_validation_fields(error, field)

    def test_20_utf8_password_byte_boundary(self):
        email = f"e2e-{RUN}-utf8@example.test"
        too_long = "я" * 37  # 37 characters pass maxLength=72, but UTF-8 is 74 bytes.
        for path in ("/auth/register", "/auth/login"):
            body = {"email": email, "password": too_long}
            if path.endswith("register"):
                body["role"] = "USER"
            error = self.anonymous.request("POST", path, body, expected=400, error="VALIDATION_ERROR")
            self.assert_validation_fields(error, "password")
        valid = "я" * 36  # Exactly 72 UTF-8 bytes remains usable.
        self.anonymous.request("POST", "/auth/register", {"email": email, "password": valid, "role": "USER"}, expected=201)
        tokens = self.anonymous.request("POST", "/auth/login", {"email": email, "password": valid})
        Client(tokens["access_token"]).request("GET", "/products")

    def test_21_expired_signed_jwt_fixture(self):
        if not OPTIONS.jwt_secret:
            self.skipTest("set JWT_SECRET (local test signing key) to test expired signed JWTs")
        _, _, tokens = self.account()
        def decode(part):
            return json.loads(base64.urlsafe_b64decode(part + "=" * (-len(part) % 4)))
        def encode(value):
            return base64.urlsafe_b64encode(json.dumps(value, separators=(",", ":")).encode()).rstrip(b"=").decode()
        def expired(token):
            header_part, claims_part, signature_part = token.split(".")
            algorithm = decode(header_part)["alg"]
            digest = {"HS256": hashlib.sha256, "HS384": hashlib.sha384, "HS512": hashlib.sha512}[algorithm]
            secret = OPTIONS.jwt_secret.encode()
            original_signature = hmac.new(secret, (header_part + "." + claims_part).encode(), digest).digest()
            supplied_signature = base64.urlsafe_b64decode(signature_part + "=" * (-len(signature_part) % 4))
            self.assertTrue(hmac.compare_digest(original_signature, supplied_signature),
                            "JWT_SECRET does not match the local API signing key")
            claims = decode(claims_part)
            claims.update(iat=int(time.time()) - 3600, exp=int(time.time()) - 60)
            payload = header_part + "." + encode(claims)
            signature = base64.urlsafe_b64encode(hmac.new(secret, payload.encode(), digest).digest()).rstrip(b"=").decode()
            return payload + "." + signature
        Client(expired(tokens["access_token"])).request("GET", "/products", expected=401, error="TOKEN_EXPIRED")
        self.anonymous.request("POST", "/auth/refresh", {"refresh_token": expired(tokens["refresh_token"])},
                               expected=401, error="REFRESH_TOKEN_INVALID")


def main():
    global OPTIONS
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", default=os.environ.get("API_BASE_URL", "http://localhost:8080"))
    parser.add_argument("--admin-email", default=os.environ.get("ADMIN_EMAIL", "admin@example.test"))
    parser.add_argument("--admin-password", default=os.environ.get("ADMIN_PASSWORD", "LocalAdminPass123!"))
    parser.add_argument("--db-checks", action="store_true", help="development DB: SELECT assertions plus targeted test-data updates")
    parser.add_argument("--jwt-secret", default=os.environ.get("JWT_SECRET"),
                        help="optional local test signing key; prefer JWT_SECRET environment variable")
    OPTIONS, remaining = parser.parse_known_args()
    print(f"E2E run {RUN}; URL={OPTIONS.base_url}; db_checks={OPTIONS.db_checks}", flush=True)
    unittest.main(argv=[sys.argv[0]] + remaining, verbosity=2)


if __name__ == "__main__":
    main()
