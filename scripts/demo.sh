#!/usr/bin/env bash
# End-to-end defense demonstration; creates only uniquely named demo data.
set -euo pipefail
cd "$(dirname "$0")/.."
demo_db=true
for option in "$@"; do
  case "$option" in
    --start) docker compose up --build --detach --wait ;;
    --no-db) demo_db=false ;;
    *) printf 'Usage: bash scripts/demo.sh [--start] [--no-db]\n' >&2; exit 2 ;;
  esac
done
DEMO_DB="$demo_db" python3 - <<'PY'
import argparse
import json
import os
import subprocess
import sys
import uuid
from datetime import datetime, timedelta, timezone

sys.path.insert(0, "scripts")
import e2e

e2e.OPTIONS = argparse.Namespace(base_url=os.getenv("API_BASE_URL", "http://localhost:8080"))
anonymous = e2e.Client()
run = uuid.uuid4().hex[:8]

def show(label, value):
    print("\n" + label, flush=True)
    print(json.dumps(value, ensure_ascii=False, indent=2), flush=True)

def account(role):
    credentials = {"email": f"demo-{run}-{role.lower()}@example.test", "password": "DemoPassword123!"}
    user = anonymous.request("POST", "/auth/register", dict(credentials, role=role), expected=201)
    token = anonymous.request("POST", "/auth/login", credentials)
    show(f"REGISTER {role} → LOGIN (токены не печатаются)", user)
    return e2e.Client(token["access_token"]), user

seller, seller_info = account("SELLER")
buyer, buyer_info = account("USER")
payload = {"name": "Demo " + run, "description": "Снапшот цены и резервирование",
           "price": 100, "stock": 5, "category": "DEMO_" + run, "status": "ACTIVE"}
product = seller.request("POST", "/products", payload, expected=201)
show("POST /products → stock=5, price=100", product)
now = datetime.now(timezone.utc)
promo = seller.request("POST", "/promo-codes", {
    "code": "DEMO" + run.upper(), "discount_type": "PERCENTAGE", "discount_value": 10,
    "min_order_amount": 100, "max_uses": 1,
    "valid_from": (now - timedelta(days=1)).isoformat(),
    "valid_until": (now + timedelta(days=1)).isoformat(), "active": True,
}, expected=201)
show("POST /promo-codes → скидка 10%, max_uses=1", promo)
order = buyer.request("POST", "/orders", {
    "items": [{"product_id": product["id"], "quantity": 2}], "promo_code": promo["code"],
}, expected=201)
show("POST /orders → 2 × 100 − 10% = 180", order)
payload.update(price=150, stock=3)
seller.request("PUT", f"/products/{product['id']}", payload)
show("GET /orders: цена товара уже 150, price_at_order всё ещё 100",
     buyer.request("GET", f"/orders/{order['id']}"))
updated = buyer.request("PUT", f"/orders/{order['id']}", {
    "items": [{"product_id": product["id"], "quantity": 3}],
})
show("PUT /orders → 3 × snapshot 100 − 10% = 270; промокод не расходуется повторно", updated)
show("Повторный CREATE: 429 ORDER_LIMIT_EXCEEDED",
     buyer.request("POST", "/orders", {"items": [{"product_id": product["id"], "quantity": 1}]},
                   expected=429, error="ORDER_LIMIT_EXCEEDED"))

def database(label):
    if os.getenv("DEMO_DB") != "true":
        return
    statements = [
        f"SELECT id,name,price,stock,status,seller_id FROM products WHERE id={int(product['id'])};",
        f"SELECT id,user_id,status,total_amount,discount_amount,promo_code_id FROM orders WHERE id={int(order['id'])};",
        f"SELECT product_id,quantity,price_at_order FROM order_items WHERE order_id={int(order['id'])};",
        f"SELECT code,max_uses,current_uses FROM promo_codes WHERE id={int(promo['id'])};",
        f"SELECT user_id,operation_type,created_at FROM user_operations WHERE user_id={int(buyer_info['id'])} ORDER BY id;",
    ]
    print("\n" + label, flush=True)
    for query in statements:
        print("\n" + query, flush=True)
        subprocess.run(["docker", "compose", "exec", "-T", "db", "psql", "-X", "-U", "marketplace",
                        "-d", "marketplace", "-v", "ON_ERROR_STOP=1", "-c", query], check=True)

database("SELECT до отмены: stock=2, CREATED, total=270, current_uses=1")
show("POST /orders/{id}/cancel → CANCELED",
     buyer.request("POST", f"/orders/{order['id']}/cancel"))
seller.request("DELETE", f"/products/{product['id']}", expected=204)
show("DELETE мягкий: GET всё ещё находит товар со статусом ARCHIVED",
     seller.request("GET", f"/products/{product['id']}"))
show("Невалидное количество → 400 VALIDATION_ERROR с details",
     buyer.request("POST", "/orders", {"items": [{"product_id": product["id"], "quantity": 0}]},
                   expected=400, error="VALIDATION_ERROR"))
database("SELECT после отмены: stock=5, CANCELED, ARCHIVED, current_uses=0")
print("\nДемо завершено. Созданные данные сохранены для разбора на защите.", flush=True)
PY
