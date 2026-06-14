// Package orders implementa o serviço de pedidos: guarda os pedidos
// no SQLite e expõe duas rotas — uma pra criar pedido (o usuário vem
// do JWT, nunca do corpo da request) e outra pra listar pedidos de
// um usuário.
//
// O serviço de propósito não chama o de produtos pra validar o
// productId. Se chamasse, uma queda do produtos derrubaria a criação
// de pedidos junto. Esse trade-off tá no relatório.
package orders

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const ordersSchemaStatement = `
CREATE TABLE IF NOT EXISTS orders (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     INTEGER NOT NULL,
    product_id  INTEGER NOT NULL,
    created_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_orders_user_id ON orders(user_id);
`

// OrderRecord é o JSON devolvido pelas duas rotas.
type OrderRecord struct {
	ID        int       `json:"id"`
	UserID    int       `json:"userId"`
	ProductID int       `json:"productId"`
	CreatedAt time.Time `json:"createdAt"`
}

// ErrInvalidOrder quando o input do Create tá claramente errado.
var ErrInvalidOrder = errors.New("invalid order")

// Store é o wrapper em volta do SQLite do serviço de pedidos.
type Store struct {
	database *sql.DB
}

// OpenStore abre (ou cria) o SQLite e aplica o schema.
func OpenStore(databaseFilePath string) (*Store, error) {
	connectionString := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", databaseFilePath)
	databaseHandle, openError := sql.Open("sqlite", connectionString)
	if openError != nil {
		return nil, fmt.Errorf("opening orders database: %w", openError)
	}
	if pingError := databaseHandle.Ping(); pingError != nil {
		_ = databaseHandle.Close()
		return nil, fmt.Errorf("pinging orders database: %w", pingError)
	}
	if _, schemaError := databaseHandle.Exec(ordersSchemaStatement); schemaError != nil {
		_ = databaseHandle.Close()
		return nil, fmt.Errorf("applying orders schema: %w", schemaError)
	}
	return &Store{database: databaseHandle}, nil
}

// Close fecha o handle do banco. Pode ser chamado mais de uma vez.
func (store *Store) Close() error {
	if store == nil || store.database == nil {
		return nil
	}
	return store.database.Close()
}

// Create grava um pedido novo. O created_at é definido aqui em UTC.
func (store *Store) Create(callContext context.Context, userID, productID int) (OrderRecord, error) {
	if userID <= 0 || productID <= 0 {
		return OrderRecord{}, fmt.Errorf("%w: user id and product id must be positive", ErrInvalidOrder)
	}
	createdAt := time.Now().UTC()

	insertResult, insertError := store.database.ExecContext(
		callContext,
		`INSERT INTO orders (user_id, product_id, created_at) VALUES (?, ?, ?)`,
		userID, productID, createdAt.Format(time.RFC3339Nano),
	)
	if insertError != nil {
		return OrderRecord{}, fmt.Errorf("inserting order: %w", insertError)
	}
	insertedID, lastIDError := insertResult.LastInsertId()
	if lastIDError != nil {
		return OrderRecord{}, fmt.Errorf("retrieving inserted order id: %w", lastIDError)
	}
	return OrderRecord{
		ID:        int(insertedID),
		UserID:    userID,
		ProductID: productID,
		CreatedAt: createdAt,
	}, nil
}

// ListByUserID devolve todos os pedidos de um usuário, do mais antigo
// pro mais novo.
func (store *Store) ListByUserID(callContext context.Context, userID int) ([]OrderRecord, error) {
	rows, queryError := store.database.QueryContext(
		callContext,
		`SELECT id, user_id, product_id, created_at FROM orders WHERE user_id = ? ORDER BY id ASC`,
		userID,
	)
	if queryError != nil {
		return nil, fmt.Errorf("listing orders for user: %w", queryError)
	}
	defer func() { _ = rows.Close() }()

	collected := make([]OrderRecord, 0)
	for rows.Next() {
		var current OrderRecord
		var createdAtRaw string
		if scanError := rows.Scan(&current.ID, &current.UserID, &current.ProductID, &createdAtRaw); scanError != nil {
			return nil, fmt.Errorf("scanning order row: %w", scanError)
		}
		parsedTime, parseError := time.Parse(time.RFC3339Nano, createdAtRaw)
		if parseError != nil {
			return nil, fmt.Errorf("parsing stored created_at: %w", parseError)
		}
		current.CreatedAt = parsedTime
		collected = append(collected, current)
	}
	if iterationError := rows.Err(); iterationError != nil {
		return nil, fmt.Errorf("iterating orders: %w", iterationError)
	}
	return collected, nil
}
