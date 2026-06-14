package users

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/filipe-ms/distributed-ecommerce/internal/authentication"

	_ "modernc.org/sqlite"
)

// schemaStatement é o CREATE TABLE que roda no startup. Como tem
// IF NOT EXISTS, é idempotente.
const schemaStatement = `
CREATE TABLE IF NOT EXISTS users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    name          TEXT NOT NULL,
    email         TEXT NOT NULL UNIQUE COLLATE NOCASE,
    password_hash TEXT NOT NULL,
    role          TEXT NOT NULL DEFAULT 'user'
);
`

// ErrEmailAlreadyRegistered acontece quando o INSERT bate na constraint
// UNIQUE do email. No HTTP isso vira 409.
var ErrEmailAlreadyRegistered = errors.New("email is already registered")

// ErrUserNotFound quando o GetByID/GetByEmail não encontra ninguém.
var ErrUserNotFound = errors.New("user not found")

// Store é o wrapper em volta do SQLite. database/sql já cuida do pool
// de conexões, então é seguro pra usar com várias goroutines.
type Store struct {
	database *sql.DB
}

// OpenStore abre (ou cria) o arquivo SQLite, aplica o schema e
// devolve um Store pronto pra usar. Os pragmas (WAL, busy_timeout)
// são recomendados pra qualquer aplicação com múltiplos writers.
func OpenStore(databaseFilePath string) (*Store, error) {
	connectionString := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", databaseFilePath)
	databaseHandle, openError := sql.Open("sqlite", connectionString)
	if openError != nil {
		return nil, fmt.Errorf("opening users database: %w", openError)
	}
	if pingError := databaseHandle.Ping(); pingError != nil {
		_ = databaseHandle.Close()
		return nil, fmt.Errorf("pinging users database: %w", pingError)
	}
	if _, schemaError := databaseHandle.Exec(schemaStatement); schemaError != nil {
		_ = databaseHandle.Close()
		return nil, fmt.Errorf("applying users schema: %w", schemaError)
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

// CreateUser salva um usuário novo. A senha é hasheada aqui dentro,
// então quem chama não precisa pensar em bcrypt. Devolve
// ErrEmailAlreadyRegistered se o email já existe.
func (store *Store) CreateUser(callContext context.Context, name, email, plainPassword, role string) (PublicUserView, error) {
	hashedPassword, hashError := authentication.HashPassword(plainPassword)
	if hashError != nil {
		return PublicUserView{}, hashError
	}
	insertResult, insertError := store.database.ExecContext(
		callContext,
		`INSERT INTO users (name, email, password_hash, role) VALUES (?, ?, ?, ?)`,
		name, email, hashedPassword, role,
	)
	if insertError != nil {
		if isUniqueConstraintViolation(insertError) {
			return PublicUserView{}, ErrEmailAlreadyRegistered
		}
		return PublicUserView{}, fmt.Errorf("inserting user: %w", insertError)
	}
	insertedID, lastIDError := insertResult.LastInsertId()
	if lastIDError != nil {
		return PublicUserView{}, fmt.Errorf("retrieving inserted user id: %w", lastIDError)
	}
	return PublicUserView{
		ID:    int(insertedID),
		Name:  name,
		Email: email,
		Role:  role,
	}, nil
}

// GetByEmail devolve a linha completa (com hash) pelo email. O hash só
// é usado no handler de login.
func (store *Store) GetByEmail(callContext context.Context, email string) (storedUserRecord, error) {
	row := store.database.QueryRowContext(
		callContext,
		`SELECT id, name, email, password_hash, role FROM users WHERE email = ? COLLATE NOCASE`,
		email,
	)
	var record storedUserRecord
	scanError := row.Scan(&record.ID, &record.Name, &record.Email, &record.PasswordHash, &record.Role)
	if errors.Is(scanError, sql.ErrNoRows) {
		return storedUserRecord{}, ErrUserNotFound
	}
	if scanError != nil {
		return storedUserRecord{}, fmt.Errorf("scanning user row: %w", scanError)
	}
	return record, nil
}

// GetByID devolve a versão pública do usuário com aquele id.
func (store *Store) GetByID(callContext context.Context, userID int) (PublicUserView, error) {
	row := store.database.QueryRowContext(
		callContext,
		`SELECT id, name, email, role FROM users WHERE id = ?`,
		userID,
	)
	var view PublicUserView
	scanError := row.Scan(&view.ID, &view.Name, &view.Email, &view.Role)
	if errors.Is(scanError, sql.ErrNoRows) {
		return PublicUserView{}, ErrUserNotFound
	}
	if scanError != nil {
		return PublicUserView{}, fmt.Errorf("scanning user row: %w", scanError)
	}
	return view, nil
}

// CountUsers conta quantas linhas existem na tabela. Usado no startup
// pra decidir se já tem que criar o admin padrão.
func (store *Store) CountUsers(callContext context.Context) (int, error) {
	var count int
	if scanError := store.database.QueryRowContext(callContext, `SELECT COUNT(*) FROM users`).Scan(&count); scanError != nil {
		return 0, fmt.Errorf("counting users: %w", scanError)
	}
	return count, nil
}

// EnsureDefaultAccountsExist garante que existam um admin e um usuário
// padrão pra demonstração. A checagem é por email, então rodar várias
// vezes não duplica registro nem sobrescreve quem já existe.
func (store *Store) EnsureDefaultAccountsExist(callContext context.Context, adminEmail, adminPassword, userEmail, userPassword string) error {
	if seedError := store.ensureUser(callContext, "Default Administrator", adminEmail, adminPassword, authentication.RoleAdministrator); seedError != nil {
		return fmt.Errorf("seeding default administrator: %w", seedError)
	}
	if seedError := store.ensureUser(callContext, "Default User", userEmail, userPassword, authentication.RoleUser); seedError != nil {
		return fmt.Errorf("seeding default user: %w", seedError)
	}
	return nil
}

// ensureUser cria o usuário se ele não existir pelo email. Se já
// existe, é no-op.
func (store *Store) ensureUser(callContext context.Context, name, email, plainPassword, role string) error {
	if _, lookupError := store.GetByEmail(callContext, email); lookupError == nil {
		return nil
	} else if !errors.Is(lookupError, ErrUserNotFound) {
		return lookupError
	}
	if _, createError := store.CreateUser(callContext, name, email, plainPassword, role); createError != nil {
		return createError
	}
	return nil
}

// isUniqueConstraintViolation identifica o erro do driver quando o
// INSERT bate em um índice UNIQUE. A gente compara a mensagem em vez
// de importar o tipo específico do driver.
func isUniqueConstraintViolation(databaseError error) bool {
	if databaseError == nil {
		return false
	}
	message := databaseError.Error()
	return strings.Contains(message, "UNIQUE") && strings.Contains(message, "constraint")
}
