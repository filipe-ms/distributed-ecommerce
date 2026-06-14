// Package users implementa o serviço de usuários: cadastro, login
// (que devolve um JWT) e a busca de usuário por id (atrás de
// autenticação). O ponto de entrada é a função BuildRouter no
// server.go; o resto é interno do pacote.
package users

// PublicUserView é o JSON devolvido pros clientes. De propósito não
// inclui o hash bcrypt pra não vazar nada de senha em log.
type PublicUserView struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	Role  string `json:"role"`
}

// storedUserRecord é a linha da tabela "users". Tem o hash bcrypt e
// não é serializado pra JSON.
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

// registerRequestPayload é o corpo JSON do POST /users/register.
type registerRequestPayload struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

// loginRequestPayload é o corpo JSON do POST /users/login.
type loginRequestPayload struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// loginResponsePayload é o que /users/login devolve: o JWT assinado e
// os dados do usuário (pra evitar uma segunda request do dashboard).
type loginResponsePayload struct {
	Token string         `json:"token"`
	User  PublicUserView `json:"user"`
}
