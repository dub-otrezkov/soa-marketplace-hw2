package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/dub-otrezkov/soa-marketplace-hw2/internal/api"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

const productColumns = "id, name, description, price::text, stock, category, status, seller_id, created_at, updated_at"

// SQL returns NUMERIC as text, keeping the path from PostgreSQL to JSON decimal
// exact. ProductResponse is generated from OpenAPI, not a parallel handwritten DTO.
func scanProduct(row interface{ Scan(...any) error }) (api.ProductResponse, error) {
	var product api.ProductResponse
	var price, status string
	err := row.Scan(&product.Id, &product.Name, &product.Description, &price,
		&product.Stock, &product.Category, &status, &product.SellerId,
		&product.CreatedAt, &product.UpdatedAt)
	if err != nil {
		return product, err
	}
	product.Price, err = decimal.NewFromString(price)
	product.Status = api.ProductStatus(status)
	return product, err
}

func productInputError(field, message string) error {
	return apiError(http.StatusBadRequest, "VALIDATION_ERROR", "Invalid product input", map[string]any{
		"fields": []map[string]string{{"field": field, "message": message}},
	})
}

func validateProduct(name string, description *string, price decimal.Decimal, stock int32,
	category string, status api.ProductStatus, sellerID *int64) error {
	if utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 255 || strings.TrimSpace(name) == "" {
		return productInputError("name", "must contain 1 to 255 characters and non-whitespace text")
	}
	if description != nil && utf8.RuneCountInString(*description) > 4000 {
		return productInputError("description", "must contain at most 4000 characters")
	}
	if price.LessThan(decimal.RequireFromString("0.01")) || price.GreaterThan(decimal.RequireFromString("9999999999.99")) || !price.Equal(price.Truncate(2)) {
		return productInputError("price", "must be between 0.01 and 9999999999.99 with at most two decimal places")
	}
	if stock < 0 {
		return productInputError("stock", "must be nonnegative")
	}
	if utf8.RuneCountInString(category) < 1 || utf8.RuneCountInString(category) > 100 || strings.TrimSpace(category) == "" {
		return productInputError("category", "must contain 1 to 100 characters and non-whitespace text")
	}
	switch string(status) {
	case "ACTIVE", "INACTIVE", "ARCHIVED":
	default:
		return productInputError("status", "must be ACTIVE, INACTIVE or ARCHIVED")
	}
	if sellerID != nil && *sellerID < 1 {
		return productInputError("seller_id", "must be a positive identifier")
	}
	return nil
}

func requireProductWriter(actor Principal) error {
	if actor.Role != "SELLER" && actor.Role != "ADMIN" {
		return apiError(http.StatusForbidden, "ACCESS_DENIED", "Role does not allow product changes", nil)
	}
	return nil
}

func productCreateSeller(actor Principal, requested *int64) (int64, error) {
	if err := requireProductWriter(actor); err != nil {
		return 0, err
	}
	sellerID := actor.ID
	if requested != nil {
		sellerID = *requested
	}
	if actor.Role != "ADMIN" && sellerID != actor.ID {
		return 0, apiError(http.StatusForbidden, "ACCESS_DENIED", "Seller may create only their own products", nil)
	}
	return sellerID, nil
}

func productUpdateSeller(actor Principal, currentSeller int64, requested *int64) (int64, error) {
	if err := requireProductWriter(actor); err != nil {
		return 0, err
	}
	if actor.Role != "ADMIN" && actor.ID != currentSeller {
		return 0, apiError(http.StatusForbidden, "ACCESS_DENIED", "Product belongs to another seller", nil)
	}
	sellerID := currentSeller
	if requested != nil {
		sellerID = *requested
	}
	if actor.Role != "ADMIN" && sellerID != actor.ID {
		return 0, apiError(http.StatusForbidden, "ACCESS_DENIED", "Seller cannot transfer a product", nil)
	}
	return sellerID, nil
}

func validateProductSeller(ctx context.Context, tx pgx.Tx, sellerID int64) error {
	var valid bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM users WHERE id=$1 AND role IN ('SELLER','ADMIN'))", sellerID).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return productInputError("seller_id", "must identify an existing SELLER or ADMIN")
	}
	return nil
}

func productNotFound(id int64) error {
	return apiError(http.StatusNotFound, "PRODUCT_NOT_FOUND", "Product not found", map[string]any{"product_id": id})
}

func (s *Server) CreateProduct(w http.ResponseWriter, r *http.Request) {
	actor := principal(r)
	if err := requireProductWriter(actor); err != nil {
		writeError(w, err)
		return
	}
	var input api.ProductCreate
	if err := decodeBody(r, &input); err != nil {
		writeError(w, err)
		return
	}
	if err := validateProduct(input.Name, input.Description, input.Price, input.Stock, input.Category, input.Status, input.SellerId); err != nil {
		writeError(w, err)
		return
	}
	sellerID, err := productCreateSeller(actor, input.SellerId)
	if err != nil {
		writeError(w, err)
		return
	}
	var result api.ProductResponse
	err = s.inTx(r.Context(), func(tx pgx.Tx) error {
		if err := validateProductSeller(r.Context(), tx, sellerID); err != nil {
			return err
		}
		var err error
		result, err = scanProduct(tx.QueryRow(r.Context(), `INSERT INTO products
			(name, description, price, stock, category, status, seller_id)
			VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING `+productColumns,
			input.Name, input.Description, input.Price.String(), input.Stock, input.Category, string(input.Status), sellerID))
		return err
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (s *Server) GetProduct(w http.ResponseWriter, r *http.Request, id int64) {
	if id < 1 {
		writeError(w, productInputError("id", "must be a positive identifier"))
		return
	}
	product, err := scanProduct(s.pool.QueryRow(r.Context(), "SELECT "+productColumns+" FROM products WHERE id=$1", id))
	if errors.Is(err, pgx.ErrNoRows) {
		err = productNotFound(id)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, product)
}

func productPagination(params api.ListProductsParams) (int32, int32, error) {
	page, size := int32(0), int32(20)
	if params.Page != nil {
		page = *params.Page
	}
	if params.Size != nil {
		size = *params.Size
	}
	if page < 0 {
		return 0, 0, productInputError("page", "must be nonnegative")
	}
	if size < 1 || size > 100 {
		return 0, 0, productInputError("size", "must be between 1 and 100")
	}
	if params.Status != nil {
		switch string(*params.Status) {
		case "ACTIVE", "INACTIVE", "ARCHIVED":
		default:
			return 0, 0, productInputError("status", "must be ACTIVE, INACTIVE or ARCHIVED")
		}
	}
	if params.Category != nil && (utf8.RuneCountInString(*params.Category) < 1 || utf8.RuneCountInString(*params.Category) > 100) {
		return 0, 0, productInputError("category", "must contain 1 to 100 characters")
	}
	return page, size, nil
}

func (s *Server) ListProducts(w http.ResponseWriter, r *http.Request, params api.ListProductsParams) {
	page, size, err := productPagination(params)
	if err != nil {
		writeError(w, err)
		return
	}
	ctx := r.Context()
	// Count and page use the same snapshot even if another transaction changes
	// products between the two queries.
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		writeError(w, err)
		return
	}
	defer tx.Rollback(ctx)
	where := " WHERE TRUE"
	args := make([]any, 0, 4)
	if params.Status != nil {
		args = append(args, string(*params.Status))
		where += fmt.Sprintf(" AND status=$%d", len(args))
	}
	if params.Category != nil {
		args = append(args, *params.Category)
		where += fmt.Sprintf(" AND category=$%d", len(args))
	}
	result := api.ProductPage{Items: []api.ProductResponse{}, Page: page, Size: size}
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM products"+where, args...).Scan(&result.TotalElements); err != nil {
		writeError(w, err)
		return
	}
	args = append(args, size, int64(page)*int64(size))
	query := "SELECT " + productColumns + " FROM products" + where + fmt.Sprintf(" ORDER BY id LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		writeError(w, err)
		return
	}
	for rows.Next() {
		product, scanErr := scanProduct(rows)
		if scanErr != nil {
			rows.Close()
			writeError(w, scanErr)
			return
		}
		result.Items = append(result.Items, product)
	}
	err = rows.Err()
	rows.Close()
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) UpdateProduct(w http.ResponseWriter, r *http.Request, id int64) {
	actor := principal(r)
	if err := requireProductWriter(actor); err != nil {
		writeError(w, err)
		return
	}
	if id < 1 {
		writeError(w, productInputError("id", "must be a positive identifier"))
		return
	}
	var input api.ProductUpdate
	if err := decodeBody(r, &input); err != nil {
		writeError(w, err)
		return
	}
	if err := validateProduct(input.Name, input.Description, input.Price, input.Stock, input.Category, input.Status, input.SellerId); err != nil {
		writeError(w, err)
		return
	}
	var result api.ProductResponse
	err := s.inTx(r.Context(), func(tx pgx.Tx) error {
		current, err := scanProduct(tx.QueryRow(r.Context(), "SELECT "+productColumns+" FROM products WHERE id=$1 FOR UPDATE", id))
		if errors.Is(err, pgx.ErrNoRows) {
			return productNotFound(id)
		}
		if err != nil {
			return err
		}
		sellerID, err := productUpdateSeller(actor, current.SellerId, input.SellerId)
		if err != nil {
			return err
		}
		if err := validateProductSeller(r.Context(), tx, sellerID); err != nil {
			return err
		}
		result, err = scanProduct(tx.QueryRow(r.Context(), `UPDATE products SET name=$1,
			description=$2, price=$3, stock=$4, category=$5, status=$6, seller_id=$7
			WHERE id=$8 RETURNING `+productColumns,
			input.Name, input.Description, input.Price.String(), input.Stock, input.Category, string(input.Status), sellerID, id))
		return err
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) ArchiveProduct(w http.ResponseWriter, r *http.Request, id int64) {
	actor := principal(r)
	if err := requireProductWriter(actor); err != nil {
		writeError(w, err)
		return
	}
	if id < 1 {
		writeError(w, productInputError("id", "must be a positive identifier"))
		return
	}
	err := s.inTx(r.Context(), func(tx pgx.Tx) error {
		current, err := scanProduct(tx.QueryRow(r.Context(), "SELECT "+productColumns+" FROM products WHERE id=$1 FOR UPDATE", id))
		if errors.Is(err, pgx.ErrNoRows) {
			return productNotFound(id)
		}
		if err != nil {
			return err
		}
		if _, err := productUpdateSeller(actor, current.SellerId, nil); err != nil {
			return err
		}
		_, err = tx.Exec(r.Context(), "UPDATE products SET status='ARCHIVED' WHERE id=$1", id)
		return err
	})
	if err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
