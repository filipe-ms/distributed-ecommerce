// Package authentication junta o que tem a ver com login dos usuários:
// hash de senha (bcrypt), geração e validação de JWT, e os middlewares
// que protegem as rotas. Todos os serviços importam daqui pra usar a
// mesma chave secreta e o mesmo formato de token.
package authentication

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/filipe-ms/distributed-ecommerce/internal/httpjson"
)

// Os dois únicos papéis que existem no sistema.
const (
	RoleUser          = "user"
	RoleAdministrator = "admin"
)

// Algoritmo HMAC fixo. Travar isso evita o ataque clássico de "alg: none".
var signingMethod = jwt.SigningMethodHS256

const bcryptCost = bcrypt.DefaultCost

// Claims é o que vai dentro do JWT. Os campos userId, email e role são
// os que o enunciado pediu; o RegisteredClaims dá os campos padrão como
// exp, iat e iss.
type Claims struct {
	UserID int    `json:"userId"`
	Email  string `json:"email"`
	Role   string `json:"role"`
	jwt.RegisteredClaims
}

// HashPassword gera o hash bcrypt da senha em texto puro.
func HashPassword(plainPassword string) (string, error) {
	hashedBytes, hashError := bcrypt.GenerateFromPassword([]byte(plainPassword), bcryptCost)
	if hashError != nil {
		return "", fmt.Errorf("hashing password: %w", hashError)
	}
	return string(hashedBytes), nil
}

// VerifyPassword compara a senha digitada com o hash salvo no banco.
func VerifyPassword(plainPassword, bcryptHash string) error {
	if compareError := bcrypt.CompareHashAndPassword([]byte(bcryptHash), []byte(plainPassword)); compareError != nil {
		return fmt.Errorf("verifying password: %w", compareError)
	}
	return nil
}

// SignToken monta um JWT com os dados do usuário e devolve a string
// assinada. tokenLifetime define quanto tempo o token vai ser válido.
func SignToken(signingSecret []byte, userID int, email, role string, tokenLifetime time.Duration) (string, error) {
	if len(signingSecret) == 0 {
		return "", errors.New("signing secret is empty")
	}
	now := time.Now().UTC()
	claims := Claims{
		UserID: userID,
		Email:  email,
		Role:   role,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "distributed-ecommerce",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(tokenLifetime)),
		},
	}
	signedToken, signError := jwt.NewWithClaims(signingMethod, claims).SignedString(signingSecret)
	if signError != nil {
		return "", fmt.Errorf("signing token: %w", signError)
	}
	return signedToken, nil
}

// VerifyToken confere se o token é válido (assinatura certa e não
// expirado) e devolve as claims já decodificadas.
func VerifyToken(signingSecret []byte, compactToken string) (*Claims, error) {
	parsedClaims := &Claims{}
	parsedToken, parseError := jwt.ParseWithClaims(compactToken, parsedClaims, func(token *jwt.Token) (any, error) {
		if token.Method != signingMethod {
			return nil, fmt.Errorf("unexpected signing method %q", token.Method.Alg())
		}
		return signingSecret, nil
	})
	if parseError != nil {
		return nil, fmt.Errorf("verifying token: %w", parseError)
	}
	if !parsedToken.Valid {
		return nil, errors.New("token is not valid")
	}
	return parsedClaims, nil
}

// Chave usada pra guardar as claims dentro do context da request.
type claimsContextKey struct{}

// ClaimsFromContext devolve as claims que o middleware salvou no
// context, ou nil se a request não passou por autenticação.
func ClaimsFromContext(requestContext context.Context) *Claims {
	value, ok := requestContext.Value(claimsContextKey{}).(*Claims)
	if !ok {
		return nil
	}
	return value
}

// RequireValidToken é o middleware que exige um JWT válido. Pega o
// header Authorization, valida, e injeta as claims no context. Se algo
// dá errado, responde 401.
func RequireValidToken(signingSecret []byte) func(http.Handler) http.Handler {
	return func(nextHandler http.Handler) http.Handler {
		return http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
			rawToken, extractionError := extractBearerToken(request)
			if extractionError != nil {
				httpjson.WriteError(responseWriter, http.StatusUnauthorized, extractionError.Error())
				return
			}
			verifiedClaims, verifyError := VerifyToken(signingSecret, rawToken)
			if verifyError != nil {
				httpjson.WriteError(responseWriter, http.StatusUnauthorized, "invalid or expired token")
				return
			}
			contextWithClaims := context.WithValue(request.Context(), claimsContextKey{}, verifiedClaims)
			nextHandler.ServeHTTP(responseWriter, request.WithContext(contextWithClaims))
		})
	}
}

// RequireAdministratorRole é o middleware que bloqueia quem não é admin.
// Roda depois do RequireValidToken, então as claims já estão no context.
func RequireAdministratorRole(nextHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		claims := ClaimsFromContext(request.Context())
		if claims == nil {
			httpjson.WriteError(responseWriter, http.StatusUnauthorized, "missing authentication")
			return
		}
		if claims.Role != RoleAdministrator {
			httpjson.WriteError(responseWriter, http.StatusForbidden, "administrator role required")
			return
		}
		nextHandler.ServeHTTP(responseWriter, request)
	})
}

// extractBearerToken pega o token do header "Authorization: Bearer <x>".
func extractBearerToken(request *http.Request) (string, error) {
	authorizationHeader := request.Header.Get("Authorization")
	if authorizationHeader == "" {
		return "", errors.New("missing Authorization header")
	}
	const bearerPrefix = "Bearer "
	if !strings.HasPrefix(authorizationHeader, bearerPrefix) {
		return "", errors.New(`Authorization header must use "Bearer " scheme`)
	}
	rawToken := strings.TrimSpace(strings.TrimPrefix(authorizationHeader, bearerPrefix))
	if rawToken == "" {
		return "", errors.New("Authorization header has no token")
	}
	return rawToken, nil
}
