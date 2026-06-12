// Package users implements the user service: registration, login (returning
// a signed JWT) and a single "get by id" lookup behind authentication. The
// public surface is the RegisterRoutes function in server.go; everything
// else is package-private.
package users

// PublicUserView is the JSON shape returned to clients. It deliberately
// omits the bcrypt hash so a leaky log line cannot accidentally expose
// password material.
type PublicUserView struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	Role  string `json:"role"`
}

// storedUserRecord is the row layout of the SQLite "users" table. It carries
// the bcrypt hash and is intentionally not serialised to JSON.
type storedUserRecord struct {
	ID           int
	Name         string
	Email        string
	PasswordHash string
	Role         string
}

func (record storedUserRecord) toPublicView() PublicUserView {
	return PublicUserView{
		ID:    record.ID,
		Name:  record.Name,
		Email: record.Email,
		Role:  record.Role,
	}
}

// registerRequestPayload is the JSON body POST /users/register accepts.
type registerRequestPayload struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

// loginRequestPayload is the JSON body POST /users/login accepts.
type loginRequestPayload struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// loginResponsePayload is what /users/login returns: the signed JWT plus the
// decoded user view, which spares the dashboard a follow-up request.
type loginResponsePayload struct {
	Token string         `json:"token"`
	User  PublicUserView `json:"user"`
}
