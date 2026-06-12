// Package authentication centralises the JWT and password handling used by
// every service in this project. Putting it in one place lets the gateway,
// the user service, the product service and the order service share a single
// signing secret, a single token shape and a single notion of "what is a
// valid request". The package is intentionally small — it owns three things:
//
//   1. Hashing user passwords with bcrypt.
//   2. Producing and verifying signed JWTs.
//   3. Two HTTP middlewares (RequireValidToken, RequireAdministratorRole)
//      that attach the verified claims to the request context.
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

// RoleUser and RoleAdministrator are the only two role values understood by
// this project. They are deliberately spelt out in full to make permission
// checks self-documenting at the call site.
const (
	RoleUser          = "user"
	RoleAdministrator = "admin"
)

// signingMethod is the only HMAC variant we accept on incoming tokens. Pinning
// it (instead of trusting the "alg" header) blocks the classic JWT attack
// where an attacker forges an "alg: none" token.
var signingMethod = jwt.SigningMethodHS256

// bcryptCost is left at the library default (currently 10). Higher costs are
// safer but slow tests dramatically; 10 is fine for an academic project and
// matches what the bcrypt authors recommend for general use.
const bcryptCost = bcrypt.DefaultCost

// Claims is the payload embedded in every JWT this project issues. The
// "userId", "email" and "role" fields are required by the assignment; the
// embedded RegisteredClaims provides the standard "exp", "iat" and "iss"
// fields that the JWT library validates automatically.
type Claims struct {
	UserID int    `json:"userId"`
	Email  string `json:"email"`
	Role   string `json:"role"`
	jwt.RegisteredClaims
}

// HashPassword returns the bcrypt hash of plainPassword. The cost factor is
// embedded in the returned string, so VerifyPassword does not need it as an
// explicit parameter.
func HashPassword(plainPassword string) (string, error) {
	hashedBytes, hashError := bcrypt.GenerateFromPassword([]byte(plainPassword), bcryptCost)
	if hashError != nil {
		return "", fmt.Errorf("hashing password: %w", hashError)
	}
	return string(hashedBytes), nil
}

// VerifyPassword reports whether plainPassword corresponds to the supplied
// bcrypt hash. It returns nil on a match and a wrapped error otherwise; the
// caller is expected to map any non-nil result to "invalid credentials"
// without leaking which of the two ingredients was wrong.
func VerifyPassword(plainPassword, bcryptHash string) error {
	if compareError := bcrypt.CompareHashAndPassword([]byte(bcryptHash), []byte(plainPassword)); compareError != nil {
		return fmt.Errorf("verifying password: %w", compareError)
	}
	return nil
}

// SignToken builds a JWT containing the assignment-required claims plus an
// expiration tokenLifetime in the future, signs it with the supplied secret
// and returns the compact serialised form.
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

// VerifyToken validates the supplied compact JWT against the secret. It
// rejects tokens that use a non-HMAC signing method, tokens whose signature
// fails to verify, and tokens whose "exp" claim has already passed.
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

// claimsContextKey is unexported so that no other package can plant a value
// of the same key into the context.
type claimsContextKey struct{}

// ClaimsFromContext returns the verified claims attached by RequireValidToken,
// or nil when the request is not authenticated.
func ClaimsFromContext(requestContext context.Context) *Claims {
	value, ok := requestContext.Value(claimsContextKey{}).(*Claims)
	if !ok {
		return nil
	}
	return value
}

// RequireValidToken is a middleware factory that returns a chi-compatible
// middleware. The middleware extracts the bearer token from the Authorization
// header, verifies it against signingSecret, and either attaches the resulting
// claims to the request context or short-circuits the request with 401.
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

// RequireAdministratorRole composes on top of RequireValidToken. It expects to
// run after RequireValidToken has populated the context, and it rejects any
// request whose claims do not carry the administrator role.
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
