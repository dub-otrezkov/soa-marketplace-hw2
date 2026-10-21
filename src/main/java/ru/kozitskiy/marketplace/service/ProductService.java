package ru.kozitskiy.marketplace.service;

import java.math.BigDecimal;
import java.util.ArrayList;
import java.util.Map;
import org.springframework.http.HttpStatus;
import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.jdbc.core.RowMapper;
import org.springframework.stereotype.Service;
import org.springframework.transaction.annotation.Isolation;
import org.springframework.transaction.annotation.Transactional;
import ru.kozitskiy.marketplace.api.model.*;
import ru.kozitskiy.marketplace.error.ApiException;
import ru.kozitskiy.marketplace.security.AuthContext;

@Service
public class ProductService {
    private final JdbcTemplate jdbc;
    public ProductService(JdbcTemplate jdbc) { this.jdbc=jdbc; }
    private final RowMapper<ProductResponse> rowMapper=(rs,n) -> {
        var p=new ProductResponse(); p.setId(rs.getLong("id")); p.setName(rs.getString("name"));
        p.setDescription(rs.getString("description")); p.setPrice(rs.getBigDecimal("price")); p.setStock(rs.getInt("stock"));
        p.setCategory(rs.getString("category")); p.setStatus(ProductStatus.fromValue(rs.getString("status")));
        p.setSellerId(rs.getLong("seller_id")); p.setCreatedAt(rs.getObject("created_at",java.time.OffsetDateTime.class));
        p.setUpdatedAt(rs.getObject("updated_at",java.time.OffsetDateTime.class)); return p;
    };
    @Transactional
    public ProductResponse create(ProductCreate input) {
        var actor=AuthContext.requireRoles("SELLER","ADMIN");
        Long sellerId=actor.id();
        if(input.getSellerId()!=null && !input.getSellerId().equals(actor.id())) {
            if(!actor.isAdmin()) throw new ApiException(HttpStatus.FORBIDDEN,"ACCESS_DENIED","Seller may create only their own products");
            sellerId=input.getSellerId();
        }
        validateSeller(sellerId); money(input.getPrice());
        Long id=jdbc.queryForObject("INSERT INTO products(name,description,price,stock,category,status,seller_id) VALUES (?,?,?,?,?,?,?) RETURNING id",Long.class,
            input.getName(),input.getDescription(),input.getPrice(),input.getStock(),input.getCategory(),input.getStatus().getValue(),sellerId);
        return find(id,false);
    }
    @Transactional(readOnly=true)
    public ProductResponse get(Long id) { AuthContext.current(); return find(id,false); }
    @Transactional(readOnly=true,isolation=Isolation.REPEATABLE_READ)
    public ProductPage list(Integer page,Integer size,ProductStatus status,String category) {
        AuthContext.current();
        int p=page==null?0:page, s=size==null?20:size;
        if(p<0 || s<1 || s>100) throw new ApiException(HttpStatus.BAD_REQUEST,"VALIDATION_ERROR","Invalid pagination",Map.of("fields",java.util.List.of("page","size")));
        StringBuilder where=new StringBuilder(" WHERE 1=1"); var params=new ArrayList<Object>();
        if(status!=null) { where.append(" AND status=?"); params.add(status.getValue()); }
        if(category!=null) { where.append(" AND category=?"); params.add(category); }
        long total=jdbc.queryForObject("SELECT count(*) FROM products"+where,Long.class,params.toArray());
        params.add(s); params.add((long)p*s);
        var items=jdbc.query("SELECT * FROM products"+where+" ORDER BY id LIMIT ? OFFSET ?",rowMapper,params.toArray());
        var result=new ProductPage(); result.setItems(items); result.setTotalElements(total); result.setPage(p); result.setSize(s); return result;
    }
    @Transactional
    public ProductResponse update(Long id,ProductUpdate input) {
        var actor=AuthContext.requireRoles("SELLER","ADMIN");
        var existing=find(id,true); owns(existing,actor);
        Long sellerId=input.getSellerId()==null?existing.getSellerId():input.getSellerId();
        if(!actor.isAdmin() && !sellerId.equals(actor.id())) throw new ApiException(HttpStatus.FORBIDDEN,"ACCESS_DENIED","Seller cannot transfer a product");
        validateSeller(sellerId); money(input.getPrice());
        jdbc.update("UPDATE products SET name=?,description=?,price=?,stock=?,category=?,status=?,seller_id=? WHERE id=?",
            input.getName(),input.getDescription(),input.getPrice(),input.getStock(),input.getCategory(),input.getStatus().getValue(),sellerId,id);
        return find(id,false);
    }
    @Transactional
    public void archive(Long id) {
        var actor=AuthContext.requireRoles("SELLER","ADMIN"); var existing=find(id,true); owns(existing,actor);
        jdbc.update("UPDATE products SET status='ARCHIVED' WHERE id=?",id);
    }
    private ProductResponse find(Long id,boolean lock) {
        var rows=jdbc.query("SELECT * FROM products WHERE id=?"+(lock?" FOR UPDATE":""),rowMapper,id);
        if(rows.isEmpty()) throw new ApiException(HttpStatus.NOT_FOUND,"PRODUCT_NOT_FOUND","Product not found",Map.of("product_id",id));
        return rows.getFirst();
    }
    private void owns(ProductResponse product,AuthContext.Actor actor) {
        if(!actor.isAdmin() && !actor.id().equals(product.getSellerId())) throw new ApiException(HttpStatus.FORBIDDEN,"ACCESS_DENIED","Product belongs to another seller");
    }
    private void validateSeller(Long id) {
        if(jdbc.queryForObject("SELECT count(*) FROM users WHERE id=? AND role IN ('SELLER','ADMIN')",Long.class,id)==0)
            throw new ApiException(HttpStatus.BAD_REQUEST,"VALIDATION_ERROR","seller_id must identify an existing SELLER or ADMIN",Map.of("field","seller_id"));
    }
    private void money(BigDecimal amount) {
        if(amount==null || amount.signum()<=0 || amount.stripTrailingZeros().scale()>2 || amount.compareTo(new BigDecimal("9999999999.99"))>0)
            throw new ApiException(HttpStatus.BAD_REQUEST,"VALIDATION_ERROR","Price must be positive, within DECIMAL(12,2), with at most two decimal places",Map.of("field","price"));
    }
}
