package jose

import (
	"context"
	"net/http"

	auth0 "github.com/auth0-community/go-auth0"
	"github.com/devopsfaith/krakend/config"
	"github.com/devopsfaith/krakend/logging"
	"github.com/devopsfaith/krakend/proxy"
	ginkrakend "github.com/devopsfaith/krakend/router/gin"
	"github.com/gin-gonic/gin"
	jose "gopkg.in/square/go-jose.v2"
)

func HandlerFactory(hf ginkrakend.HandlerFactory, logger logging.Logger, rejecter Rejecter) ginkrakend.HandlerFactory {
	return TokenSigner(TokenSignatureValidator(hf, logger, rejecter), logger)
}

func TokenSigner(hf ginkrakend.HandlerFactory, logger logging.Logger) ginkrakend.HandlerFactory {
	return func(cfg *config.EndpointConfig, prxy proxy.Proxy) gin.HandlerFunc {
		signerCfg, signer, err := newSigner(cfg)
		if err != nil {
			logger.Error(err.Error(), cfg.Endpoint)
			return hf(cfg, prxy)
		}

		return func(c *gin.Context) {
			proxyReq := ginkrakend.NewRequest(cfg.HeadersToPass)(c, cfg.QueryString)
			ctx, cancel := context.WithTimeout(c, cfg.Timeout)
			defer cancel()

			response, err := prxy(ctx, proxyReq)
			if err != nil {
				logger.Error("proxy response error:", err.Error())
				c.AbortWithStatus(http.StatusBadRequest)
				return
			}

			if response == nil {
				c.AbortWithStatus(http.StatusBadRequest)
				return
			}

			for _, key := range signerCfg.KeysToSign {
				tmp, ok := response.Data[key]
				if !ok {
					continue
				}
				data, ok := tmp.(map[string]interface{})
				if !ok {
					continue
				}
				token, err := signer(data)
				if err != nil {
					logger.Error(err.Error())
					c.AbortWithStatus(http.StatusBadRequest)
					return
				}
				response.Data[key] = token
			}

			for k, v := range response.Metadata.Headers {
				c.Header(k, v[0])
			}
			c.JSON(response.Metadata.StatusCode, response.Data)
		}
	}
}

func TokenSignatureValidator(hf ginkrakend.HandlerFactory, logger logging.Logger, rejecter Rejecter) ginkrakend.HandlerFactory {
	if rejecter == nil {
		rejecter = FixedRejecter(false)
	}
	return func(cfg *config.EndpointConfig, prxy proxy.Proxy) gin.HandlerFunc {
		handler := hf(cfg, prxy)
		scfg, err := getSignatureConfig(cfg)
		if err != nil {
			return handler
		}

		sa, ok := supportedAlgorithms[scfg.Alg]
		if !ok {
			logger.Fatal("JOSE: unknown algorithm", scfg.Alg, "defined for", cfg.Endpoint)
		}

		validator := auth0.NewValidator(
			auth0.NewConfiguration(
				secretProvider(scfg.URI, scfg.CacheEnabled),
				scfg.Audience,
				scfg.Issuer,
				sa,
			),
			nil,
		)

		return func(c *gin.Context) {
			token, err := validator.ValidateRequest(c.Request)
			if err != nil {
				c.AbortWithError(http.StatusUnauthorized, err)
				return
			}

			claims := map[string]interface{}{}
			err = validator.Claims(c.Request, token, &claims)
			if err != nil {
				c.AbortWithError(http.StatusUnauthorized, err)
				return
			}

			if rejecter.Reject(claims) {
				c.AbortWithStatus(http.StatusUnauthorized)
				return
			}

			if !canAccess(scfg.RolesKey, claims, scfg.Roles) {
				c.AbortWithStatus(http.StatusUnauthorized)
				return
			}

			handler(c)
		}
	}
}

func canAccess(roleKey string, claims map[string]interface{}, required []string) bool {
	if len(required) == 0 {
		return true
	}
	roles := []interface{}{}
	if tmp, ok := claims[roleKey]; ok {
		if v, ok := tmp.([]interface{}); ok {
			roles = v
		}
	}
	for _, role := range required {
		for _, r := range roles {
			if r.(string) == role {
				return true
			}
		}
	}
	return false
}

var supportedAlgorithms = map[string]jose.SignatureAlgorithm{
	"EdDSA": jose.EdDSA,
	"HS256": jose.HS256,
	"HS384": jose.HS384,
	"HS512": jose.HS512,
	"RS256": jose.RS256,
	"RS384": jose.RS384,
	"RS512": jose.RS512,
	"ES256": jose.ES256,
	"ES384": jose.ES384,
	"ES512": jose.ES512,
	"PS256": jose.PS256,
	"PS384": jose.PS384,
	"PS512": jose.PS512,
}
