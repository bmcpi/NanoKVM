package middleware

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	log "github.com/sirupsen/logrus"

	"github.com/tinkerbell-community/NanoKVM/server/config"
)

const cookieName = "nano-kvm-token"

type Token struct {
	Username string `json:"username"`
	jwt.RegisteredClaims
}

func CheckToken() gin.HandlerFunc {
	return func(c *gin.Context) {
		if allowByToken(c) {
			c.Next()
			return
		}

		abortUnauthorized(c)
	}
}

// CheckPageAuth protects server-rendered pages by validating the JWT
// cookie. On failure it clears the stale cookie and redirects to the
// login page. When authentication is disabled globally it passes through.
func CheckPageAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		conf := config.GetInstance()
		if conf.Authentication == "disable" {
			c.Next()
			return
		}

		cookie, err := c.Cookie(cookieName)
		if err != nil || cookie == "" {
			c.Redirect(http.StatusFound, "/auth/login")
			c.Abort()
			return
		}

		if _, err := ParseJWT(cookie); err != nil {
			// Token invalid or expired — clear it so the browser stops
			// sending the stale value on every request.
			clearAuthCookie(c)
			c.Redirect(http.StatusFound, "/auth/login")
			c.Abort()
			return
		}

		c.Next()
	}
}

func allowByToken(c *gin.Context) bool {
	conf := config.GetInstance()

	if conf.Authentication == "disable" {
		return true
	}

	cookie, err := c.Cookie(cookieName)
	if err != nil {
		return false
	}

	_, err = ParseJWT(cookie)
	return err == nil
}

func abortUnauthorized(c *gin.Context) {
	c.JSON(http.StatusUnauthorized, "unauthorized")
	c.Abort()
}

// clearAuthCookie expires the nano-kvm-token cookie.
func clearAuthCookie(c *gin.Context) {
	c.SetCookie(cookieName, "", -1, "/", "", false, false)
}

func GenerateJWT(username string) (string, error) {
	conf := config.GetInstance()

	expireDuration := time.Duration(conf.JWT.RefreshTokenDuration) * time.Second

	claims := Token{
		Username: username,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(expireDuration)),
		},
	}

	t := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	return t.SignedString([]byte(conf.JWT.SecretKey))
}

func ParseJWT(jwtToken string) (*Token, error) {
	conf := config.GetInstance()

	t, err := jwt.ParseWithClaims(jwtToken, &Token{}, func(token *jwt.Token) (interface{}, error) {
		return []byte(conf.JWT.SecretKey), nil
	})
	if err != nil {
		log.Debugf("parse jwt error: %s", err)
		return nil, err
	}

	if claims, ok := t.Claims.(*Token); ok && t.Valid {
		return claims, nil
	} else {
		return nil, err
	}
}
