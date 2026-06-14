package users

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/filipe-ms/distributed-ecommerce/internal/authentication"
	"github.com/filipe-ms/distributed-ecommerce/internal/httpjson"
)

// dependencies junta tudo que um handler precisa pra rodar. Usar uma
// struct deixa as assinaturas limpas e evita variável de pacote.
type dependencies struct {
	userStore     *Store
	signingSecret []byte
	tokenLifetime time.Duration
}

// writeRegistrationHandler implementa o POST /users/register.
func writeRegistrationHandler(deps dependencies) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		var payload registerRequestPayload
		if decodeError := httpjson.ReadJSON(request, &payload); decodeError != nil {
			httpjson.WriteError(responseWriter, http.StatusBadRequest, decodeError.Error())
			return
		}
		if validationError := validateRegistrationPayload(payload); validationError != nil {
			httpjson.WriteError(responseWriter, http.StatusBadRequest, validationError.Error())
			return
		}

		createdUser, createError := deps.userStore.CreateUser(
			request.Context(),
			strings.TrimSpace(payload.Name),
			normaliseEmail(payload.Email),
			payload.Password,
			authentication.RoleUser,
		)
		if errors.Is(createError, ErrEmailAlreadyRegistered) {
			httpjson.WriteError(responseWriter, http.StatusConflict, "email is already registered")
			return
		}
		if createError != nil {
			httpjson.WriteError(responseWriter, http.StatusInternalServerError, "could not register user")
			return
		}
		httpjson.WriteJSON(responseWriter, http.StatusCreated, createdUser)
	}
}

// writeLoginHandler implementa o POST /users/login. Confere a senha
// com bcrypt e devolve um JWT em caso de sucesso.
func writeLoginHandler(deps dependencies) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		var payload loginRequestPayload
		if decodeError := httpjson.ReadJSON(request, &payload); decodeError != nil {
			httpjson.WriteError(responseWriter, http.StatusBadRequest, decodeError.Error())
			return
		}
		if strings.TrimSpace(payload.Email) == "" || payload.Password == "" {
			httpjson.WriteError(responseWriter, http.StatusBadRequest, "email and password are required")
			return
		}

		storedRecord, lookupError := deps.userStore.GetByEmail(request.Context(), normaliseEmail(payload.Email))
		if errors.Is(lookupError, ErrUserNotFound) {
			httpjson.WriteError(responseWriter, http.StatusUnauthorized, "invalid email or password")
			return
		}
		if lookupError != nil {
			httpjson.WriteError(responseWriter, http.StatusInternalServerError, "could not authenticate")
			return
		}
		if passwordError := authentication.VerifyPassword(payload.Password, storedRecord.PasswordHash); passwordError != nil {
			httpjson.WriteError(responseWriter, http.StatusUnauthorized, "invalid email or password")
			return
		}

		signedToken, signError := authentication.SignToken(
			deps.signingSecret,
			storedRecord.ID,
			storedRecord.Email,
			storedRecord.Role,
			deps.tokenLifetime,
		)
		if signError != nil {
			httpjson.WriteError(responseWriter, http.StatusInternalServerError, "could not issue token")
			return
		}

		httpjson.WriteJSON(responseWriter, http.StatusOK, loginResponsePayload{
			Token: signedToken,
			User:  storedRecord.toPublicView(),
		})
	}
}

// writeGetUserByIDHandler implementa o GET /users/{userId}. Usuário
// comum só vê o próprio perfil; admin vê o de qualquer um.
func writeGetUserByIDHandler(deps dependencies) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		callerClaims := authentication.ClaimsFromContext(request.Context())
		if callerClaims == nil {
			httpjson.WriteError(responseWriter, http.StatusUnauthorized, "missing authentication")
			return
		}

		rawID := chi.URLParam(request, "userId")
		parsedID, parseError := strconv.Atoi(rawID)
		if parseError != nil || parsedID <= 0 {
			httpjson.WriteError(responseWriter, http.StatusBadRequest, "user id must be a positive integer")
			return
		}

		// Mesma regra de acesso do serviço de pedidos.
		if callerClaims.Role != authentication.RoleAdministrator && callerClaims.UserID != parsedID {
			httpjson.WriteError(responseWriter, http.StatusForbidden, "you can only access your own profile")
			return
		}

		view, lookupError := deps.userStore.GetByID(request.Context(), parsedID)
		if errors.Is(lookupError, ErrUserNotFound) {
			httpjson.WriteError(responseWriter, http.StatusNotFound, "user not found")
			return
		}
		if lookupError != nil {
			httpjson.WriteError(responseWriter, http.StatusInternalServerError, "could not load user")
			return
		}
		httpjson.WriteJSON(responseWriter, http.StatusOK, view)
	}
}

// writeHealthHandler responde no /health do serviço.
func writeHealthHandler() http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, _ *http.Request) {
		httpjson.WriteJSON(responseWriter, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// validateRegistrationPayload faz uma validação simples do registro.
func validateRegistrationPayload(payload registerRequestPayload) error {
	trimmedName := strings.TrimSpace(payload.Name)
	trimmedEmail := strings.TrimSpace(payload.Email)
	if trimmedName == "" {
		return errors.New("name is required")
	}
	if trimmedEmail == "" || !strings.Contains(trimmedEmail, "@") {
		return errors.New("a valid email is required")
	}
	if len(payload.Password) < 6 {
		return errors.New("password must be at least 6 characters")
	}
	return nil
}

// normaliseEmail tira espaços e deixa tudo minúsculo, pra busca não
// depender de capitalização.
func normaliseEmail(rawEmail string) string {
	return strings.ToLower(strings.TrimSpace(rawEmail))
}
