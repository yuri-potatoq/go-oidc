package oidc

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"golang.org/x/oauth2"
)

const (
	// at_hash value and access_token returned by Google.
	googleAccessTokenHash = "piwt8oCH-K2D9pXlaS1Y-w"
	googleAccessToken     = "ya29.CjHSA1l5WUn8xZ6HanHFzzdHdbXm-14rxnC7JHch9eFIsZkQEGoWzaYG4o7k5f6BnPLj"
	googleSigningAlg      = RS256
	// following values computed by own algo for regression testing
	computed384TokenHash = "_ILKVQjbEzFKNJjUKC2kz9eReYi0A9Of"
	computed512TokenHash = "Spa_APgwBrarSeQbxI-rbragXho6dqFpH5x9PqaPfUI"
)

type accessTokenTest struct {
	name        string
	tok         *IDToken
	accessToken string
	verifier    func(err error) error
}

func (a accessTokenTest) run(t *testing.T) {
	err := a.tok.VerifyAccessToken(a.accessToken)
	result := a.verifier(err)
	if result != nil {
		t.Error(result)
	}
}

func TestAccessTokenVerification(t *testing.T) {
	newToken := func(alg, atHash string) *IDToken {
		return &IDToken{sigAlgorithm: alg, AccessTokenHash: atHash}
	}
	assertNil := func(err error) error {
		if err != nil {
			return fmt.Errorf("want nil error, got %v", err)
		}
		return nil
	}
	assertMsg := func(msg string) func(err error) error {
		return func(err error) error {
			if err == nil {
				return fmt.Errorf("expected error, got success")
			}
			if err.Error() != msg {
				return fmt.Errorf("bad error message, %q, (want %q)", err.Error(), msg)
			}
			return nil
		}
	}
	tests := []accessTokenTest{
		{
			"goodRS256",
			newToken(googleSigningAlg, googleAccessTokenHash),
			googleAccessToken,
			assertNil,
		},
		{
			"goodES384",
			newToken("ES384", computed384TokenHash),
			googleAccessToken,
			assertNil,
		},
		{
			"goodPS512",
			newToken("PS512", computed512TokenHash),
			googleAccessToken,
			assertNil,
		},
		{
			"badRS256",
			newToken("RS256", computed512TokenHash),
			googleAccessToken,
			assertMsg("access token hash does not match value in ID token"),
		},
		{
			"nohash",
			newToken("RS256", ""),
			googleAccessToken,
			assertMsg("id token did not have an access token hash"),
		},
		{
			"EdDSA",
			newToken("EdDSA", computed512TokenHash),
			googleAccessToken,
			assertNil,
		},
		{
			"badSignAlgo",
			newToken("none", "xxx"),
			googleAccessToken,
			assertMsg(`oidc: unsupported signing algorithm "none"`),
		},
	}
	for _, test := range tests {
		t.Run(test.name, test.run)
	}
}

func TestNewProvider(t *testing.T) {
	tests := []struct {
		name                  string
		data                  string
		issuerURLOverride     string
		trailingSlash         bool
		wantAuthURL           string
		wantTokenURL          string
		wantDeviceAuthURL     string
		wantUserInfoURL       string
		wantIssuerURL         string
		wantAlgorithms        []string
		wantErr               bool
		customIssuerValidator IssuerValidator
	}{
		{
			name: "basic_case",
			data: `{
				"issuer": "ISSUER",
				"authorization_endpoint": "https://example.com/auth",
				"token_endpoint": "https://example.com/token",
				"jwks_uri": "https://example.com/keys",
				"id_token_signing_alg_values_supported": ["RS256"]
			}`,
			wantAuthURL:    "https://example.com/auth",
			wantTokenURL:   "https://example.com/token",
			wantAlgorithms: []string{"RS256"},
		},
		{
			name: "additional_algorithms",
			data: `{
				"issuer": "ISSUER",
				"authorization_endpoint": "https://example.com/auth",
				"token_endpoint": "https://example.com/token",
				"jwks_uri": "https://example.com/keys",
				"id_token_signing_alg_values_supported": ["RS256", "RS384", "ES256", "EdDSA"]
			}`,
			wantAuthURL:    "https://example.com/auth",
			wantTokenURL:   "https://example.com/token",
			wantAlgorithms: []string{"RS256", "RS384", "ES256", "EdDSA"},
		},
		{
			name: "unsupported_algorithms",
			data: `{
				"issuer": "ISSUER",
				"authorization_endpoint": "https://example.com/auth",
				"token_endpoint": "https://example.com/token",
				"jwks_uri": "https://example.com/keys",
				"id_token_signing_alg_values_supported": [
					"RS256", "RS384", "ES256", "HS256", "none"
				]
			}`,
			wantAuthURL:    "https://example.com/auth",
			wantTokenURL:   "https://example.com/token",
			wantAlgorithms: []string{"RS256", "RS384", "ES256"},
		},
		{
			name: "mismatched_issuer",
			data: `{
				"issuer": "https://example.com",
				"authorization_endpoint": "https://example.com/auth",
				"token_endpoint": "https://example.com/token",
				"jwks_uri": "https://example.com/keys",
				"id_token_signing_alg_values_supported": ["RS256"]
			}`,
			wantErr: true,
		},
		{
			name:              "mismatched_issuer_discovery_override",
			issuerURLOverride: "https://example.com",
			data: `{
				"issuer": "ISSUER",
				"authorization_endpoint": "https://example.com/auth",
				"token_endpoint": "https://example.com/token",
				"jwks_uri": "https://example.com/keys",
				"id_token_signing_alg_values_supported": ["RS256"]
			}`,
			wantIssuerURL:  "https://example.com",
			wantAuthURL:    "https://example.com/auth",
			wantTokenURL:   "https://example.com/token",
			wantAlgorithms: []string{"RS256"},
		},
		{
			name: "issuer_with_trailing_slash",
			data: `{
				"issuer": "ISSUER",
				"authorization_endpoint": "https://example.com/auth",
				"token_endpoint": "https://example.com/token",
				"jwks_uri": "https://example.com/keys",
				"id_token_signing_alg_values_supported": ["RS256"]
			}`,
			trailingSlash:  true,
			wantAuthURL:    "https://example.com/auth",
			wantTokenURL:   "https://example.com/token",
			wantAlgorithms: []string{"RS256"},
		},
		{
			// Test case taken directly from:
			// https://accounts.google.com/.well-known/openid-configuration
			name:              "google",
			wantAuthURL:       "https://accounts.google.com/o/oauth2/v2/auth",
			wantTokenURL:      "https://oauth2.googleapis.com/token",
			wantDeviceAuthURL: "https://oauth2.googleapis.com/device/code",
			wantUserInfoURL:   "https://openidconnect.googleapis.com/v1/userinfo",
			wantAlgorithms:    []string{"RS256"},
			data: `{
 "issuer": "ISSUER",
 "authorization_endpoint": "https://accounts.google.com/o/oauth2/v2/auth",
 "device_authorization_endpoint": "https://oauth2.googleapis.com/device/code",
 "token_endpoint": "https://oauth2.googleapis.com/token",
 "userinfo_endpoint": "https://openidconnect.googleapis.com/v1/userinfo",
 "revocation_endpoint": "https://oauth2.googleapis.com/revoke",
 "jwks_uri": "https://www.googleapis.com/oauth2/v3/certs",
 "response_types_supported": [
  "code",
  "token",
  "id_token",
  "code token",
  "code id_token",
  "token id_token",
  "code token id_token",
  "none"
 ],
 "subject_types_supported": [
  "public"
 ],
 "id_token_signing_alg_values_supported": [
  "RS256"
 ],
 "scopes_supported": [
  "openid",
  "email",
  "profile"
 ],
 "token_endpoint_auth_methods_supported": [
  "client_secret_post",
  "client_secret_basic"
 ],
 "claims_supported": [
  "aud",
  "email",
  "email_verified",
  "exp",
  "family_name",
  "given_name",
  "iat",
  "iss",
  "locale",
  "name",
  "picture",
  "sub"
 ],
 "code_challenge_methods_supported": [
  "plain",
  "S256"
 ],
 "grant_types_supported": [
  "authorization_code",
  "refresh_token",
  "urn:ietf:params:oauth:grant-type:device_code",
  "urn:ietf:params:oauth:grant-type:jwt-bearer"
 ]
}`,
		},
		{
			name: "custom_issuer_validator_trust_on_issuer",
			data: `{
				"issuer": "https://trusted-domain.com",
				"authorization_endpoint": "https://trusted-domain.com/auth",
				"token_endpoint": "https://trusted-domain.com/token",
				"jwks_uri": "https://trusted-domain.com/keys",
				"id_token_signing_alg_values_supported": ["RS256"]
			}`,
			customIssuerValidator: func(_, gotIssuer string) bool {
				return strings.Contains(gotIssuer, "trusted-domain")
			},
			wantAuthURL:    "https://trusted-domain.com/auth",
			wantTokenURL:   "https://trusted-domain.com/token",
			wantAlgorithms: []string{"RS256"},
		},
		{
			name: "custom_issuer_validator_DO_NOT_trust_on_issuer",
			data: `{
				"issuer": "https://trusted-domain.com",
				"authorization_endpoint": "https://trusted-domain/auth",
				"token_endpoint": "https://trusted-domain.com/token",
				"jwks_uri": "https://trusted-domain.com/keys",
				"id_token_signing_alg_values_supported": ["RS256"]
			}`,
			customIssuerValidator: func(_, _ string) bool {
				return false
			},
			wantErr:        true,
			wantAuthURL:    "https://trusted-domain.com/auth",
			wantTokenURL:   "https://trusted-domain.com/token",
			wantAlgorithms: []string{"RS256"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			var issuer string
			hf := func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/.well-known/openid-configuration" {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, strings.ReplaceAll(test.data, "ISSUER", issuer))
			}
			s := httptest.NewServer(http.HandlerFunc(hf))
			defer s.Close()

			issuer = s.URL
			if test.trailingSlash {
				issuer += "/"
			}

			if test.issuerURLOverride != "" {
				ctx = InsecureIssuerURLContext(ctx, test.issuerURLOverride)
			}

			if test.customIssuerValidator != nil {
				ctx = CustomIssuerValidationContext(ctx, test.customIssuerValidator)
			}

			p, err := NewProvider(ctx, issuer)
			if err != nil {
				if !test.wantErr {
					t.Errorf("NewProvider() failed: %v", err)
				}
				return
			}
			if test.wantErr {
				t.Fatalf("NewProvider(): expected error")
			}

			if test.wantIssuerURL != "" && p.issuer != test.wantIssuerURL {
				t.Errorf("NewProvider() unexpected issuer value, got=%s, want=%s",
					p.issuer, test.wantIssuerURL)
			}

			if p.authURL != test.wantAuthURL {
				t.Errorf("NewProvider() unexpected authURL value, got=%s, want=%s",
					p.authURL, test.wantAuthURL)
			}
			if p.tokenURL != test.wantTokenURL {
				t.Errorf("NewProvider() unexpected tokenURL value, got=%s, want=%s",
					p.tokenURL, test.wantTokenURL)
			}
			if p.deviceAuthURL != test.wantDeviceAuthURL {
				t.Errorf("NewProvider() unexpected deviceAuthURL value, got=%s, want=%s",
					p.deviceAuthURL, test.wantDeviceAuthURL)
			}
			if p.userInfoURL != test.wantUserInfoURL {
				t.Errorf("NewProvider() unexpected userInfoURL value, got=%s, want=%s",
					p.userInfoURL, test.wantUserInfoURL)
			}
			if !reflect.DeepEqual(p.algorithms, test.wantAlgorithms) {
				t.Errorf("NewProvider() unexpected algorithms value, got=%s, want=%s",
					p.algorithms, test.wantAlgorithms)
			}
		})
	}
}

func TestGetClient(t *testing.T) {
	ctx := context.Background()
	if c := getClient(ctx); c != nil {
		t.Errorf("cloneContext(): expected no *http.Client from empty context")
	}

	c := &http.Client{}
	ctx = ClientContext(ctx, c)
	if got := getClient(ctx); got == nil || c != got {
		t.Errorf("cloneContext(): expected *http.Client from context")
	}
}

type testServer struct {
	contentType string
	userInfo    string
}

func (ts *testServer) run(t *testing.T) string {
	newMux := http.NewServeMux()
	server := httptest.NewServer(newMux)

	// generated using mkjwk.org
	jwks := `{
		"keys": [
			{
				"kty": "RSA",
				"e": "AQAB",
				"use": "sig",
				"kid": "test",
				"alg": "RS256",
				"n": "ilhCmTGFjjIPVN7Lfdn_fvpXOlzxa3eWnQGZ_eRa2ibFB1mnqoWxZJ8fkWIVFOQpsn66bIfWjBo_OI3sE6LhhRF8xhsMxlSeRKhpsWg0klYnMBeTWYET69YEAX_rGxy0MCZlFZ5tpr56EVZ-3QLfNiR4hcviqj9F2qE6jopfywsnlulJgyMi3N3kugit_JCNBJ0yz4ndZrMozVOtGqt35HhggUgYROzX6SWHUJdPXSmbAZU-SVLlesQhPfHS8LLq0sACb9OmdcwrpEFdbGCSTUPlHGkN5h6Zy8CS4s_bCdXKkjD20jv37M3GjRQkjE8vyMxFlo_qT8F8VZlSgXYTFw"
			}
		]
	}`

	var userInfoJSON string
	if ts.userInfo != "" {
		userInfoJSON = fmt.Sprintf(`"userinfo_endpoint": "%s/userinfo",`, server.URL)
	}

	wellKnown := fmt.Sprintf(`{
		"issuer": "%[1]s",
		"authorization_endpoint": "%[1]s/auth",
		"token_endpoint": "%[1]s/token",
		"jwks_uri": "%[1]s/keys",
		%[2]s
		"id_token_signing_alg_values_supported": ["RS256"]
	}`, server.URL, userInfoJSON)

	newMux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, req *http.Request) {
		_, err := io.WriteString(w, wellKnown)
		if err != nil {
			w.WriteHeader(500)
		}
	})
	newMux.HandleFunc("/keys", func(w http.ResponseWriter, req *http.Request) {
		_, err := io.WriteString(w, jwks)
		if err != nil {
			w.WriteHeader(500)
		}
	})
	if ts.userInfo != "" {
		newMux.HandleFunc("/userinfo", func(w http.ResponseWriter, req *http.Request) {
			w.Header().Add("Content-Type", ts.contentType)
			_, err := io.WriteString(w, ts.userInfo)
			if err != nil {
				w.WriteHeader(500)
			}
		})
	}
	t.Cleanup(server.Close)
	return server.URL
}

func TestUserInfoEndpoint(t *testing.T) {

	userInfoJSON := `{
		"sub": "1234567890",
		"profile": "Joe Doe",
		"email": "joe@doe.com",
		"email_verified": true,
		"is_admin": true
	}`
	userInfoJSONCognitoVariant := `{
		"sub": "1234567890",
		"profile": "Joe Doe",
		"email": "joe@doe.com",
		"email_verified": "true",
		"is_admin": true
	}`

	tests := []struct {
		name         string
		server       testServer
		wantUserInfo UserInfo
	}{
		{
			name: "basic json userinfo",
			server: testServer{
				contentType: "application/json",
				userInfo:    userInfoJSON,
			},
			wantUserInfo: UserInfo{
				Subject:       "1234567890",
				Profile:       "Joe Doe",
				Email:         "joe@doe.com",
				EmailVerified: true,
				claims:        []byte(userInfoJSON),
			},
		},
		{
			name: "signed jwt userinfo",
			server: testServer{
				contentType: "application/jwt",
				// generated with jwt.io based on the private/public key pair
				userInfo: "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwicHJvZmlsZSI6IkpvZSBEb2UiLCJlbWFpbCI6ImpvZUBkb2UuY29tIiwiZW1haWxfdmVyaWZpZWQiOnRydWUsImlzX2FkbWluIjp0cnVlfQ.ejzc2IOLtvYp-2n5w3w4SW3rHNG9pOahnwpQCwuIaj7DvO4SxDIzeJmFPMKTJUc-1zi5T42mS4Gs2r18KWhSkk8kqYermRX0VcGEEsH0r2BG5boeza_EjCoJ5-jBPX5ODWGhu2sZIkZl29IbaVSC8jk8qKnqacchiHNmuv_xXjRsAgUsqYftrEQOxqhpfL5KN2qtgeVTczg3ABqs2-SFeEzcgA1TnA9H3AynCPCVUMFgh7xyS8jxx7DN-1vRHBySz5gNbf8z8MNx_XBLfRxxxMF24rDIE8Z2gf1DEAPr4tT38hD8ugKSE84gC3xHJWFWsRLg-Ll6OQqshs82axS00Q",
			},
			wantUserInfo: UserInfo{
				Subject:       "1234567890",
				Profile:       "Joe Doe",
				Email:         "joe@doe.com",
				EmailVerified: true,
				claims:        []byte(userInfoJSON),
			},
		},
		{
			name: "signed jwt userinfo, content-type with charset",
			server: testServer{
				contentType: "application/jwt; charset=ISO-8859-1",
				// generated with jwt.io based on the private/public key pair
				userInfo: "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwicHJvZmlsZSI6IkpvZSBEb2UiLCJlbWFpbCI6ImpvZUBkb2UuY29tIiwiZW1haWxfdmVyaWZpZWQiOnRydWUsImlzX2FkbWluIjp0cnVlfQ.ejzc2IOLtvYp-2n5w3w4SW3rHNG9pOahnwpQCwuIaj7DvO4SxDIzeJmFPMKTJUc-1zi5T42mS4Gs2r18KWhSkk8kqYermRX0VcGEEsH0r2BG5boeza_EjCoJ5-jBPX5ODWGhu2sZIkZl29IbaVSC8jk8qKnqacchiHNmuv_xXjRsAgUsqYftrEQOxqhpfL5KN2qtgeVTczg3ABqs2-SFeEzcgA1TnA9H3AynCPCVUMFgh7xyS8jxx7DN-1vRHBySz5gNbf8z8MNx_XBLfRxxxMF24rDIE8Z2gf1DEAPr4tT38hD8ugKSE84gC3xHJWFWsRLg-Ll6OQqshs82axS00Q",
			},
			wantUserInfo: UserInfo{
				Subject:       "1234567890",
				Profile:       "Joe Doe",
				Email:         "joe@doe.com",
				EmailVerified: true,
				claims:        []byte(userInfoJSON),
			},
		},
		{
			name: "basic json userinfo - cognito variant",
			server: testServer{
				contentType: "application/json",
				userInfo:    userInfoJSONCognitoVariant,
			},
			wantUserInfo: UserInfo{
				Subject:       "1234567890",
				Profile:       "Joe Doe",
				Email:         "joe@doe.com",
				EmailVerified: true,
				claims:        []byte(userInfoJSONCognitoVariant),
			},
		},
		{
			name: "signed jwt userinfo - cognito variant",
			server: testServer{
				contentType: "application/jwt",
				// generated with jwt.io based on the private/public key pair
				userInfo: "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwicHJvZmlsZSI6IkpvZSBEb2UiLCJlbWFpbCI6ImpvZUBkb2UuY29tIiwiZW1haWxfdmVyaWZpZWQiOiJ0cnVlIiwiaXNfYWRtaW4iOnRydWV9.V9j6Q208fnj7E5dhCHnAktqndvelyz6PYxmd2fLzA4ze8N770Tq9KFEE3QSM400GTxiP7tMyvBqnTj2q5Hr6DeRoy0WtLmYlnDfOJCr2qKbrPN0k94Ts9_sXAKEiJSKsTFUBHkrH4NhyWsaBaPamI8ghuqPKJ1LniNuskHUlzBmDDW4mTy15ArsaIno8S4XVn19OoqODIO30axJJxKfxEbsDR3-YW4OD9qn80Wzw0zOsGJ04NJRfO56VSprX0PhqvduOSUuHvm4cxtJIHHvj3AitrQriKZebZpXSs9PXPSPCysiQHyDz0A8y7R-sDgEhJlxe93nVbTU0itBehrbugQ",
			},
			wantUserInfo: UserInfo{
				Subject:       "1234567890",
				Profile:       "Joe Doe",
				Email:         "joe@doe.com",
				EmailVerified: true,
				claims:        []byte(userInfoJSONCognitoVariant),
			},
		},
		{
			name: "no userinfo endpoint",
			server: testServer{
				contentType: "application/json",
				userInfo:    "",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			serverURL := test.server.run(t)

			ctx := context.Background()

			provider, err := NewProvider(ctx, serverURL)
			if err != nil {
				t.Fatalf("Failed to initialize provider for test %v", err)
			}

			if test.server.userInfo == "" {
				if provider.UserInfoEndpoint() != "" {
					t.Errorf("expected UserInfoEndpoint to be empty, got %v", provider.UserInfoEndpoint())
				}

				// provider.UserInfo will error.
				return
			}

			if provider.UserInfoEndpoint() != serverURL+"/userinfo" {
				t.Errorf("expected UserInfoEndpoint to be %v , got %v", serverURL+"/userinfo", provider.UserInfoEndpoint())
			}

			fakeOauthToken := oauth2.Token{}
			info, err := provider.UserInfo(ctx, oauth2.StaticTokenSource(&fakeOauthToken))
			if err != nil {
				t.Fatalf("failed to get userinfo %v", err)
			}

			if info.Email != test.wantUserInfo.Email {
				t.Errorf("expected UserInfo to be %v , got %v", test.wantUserInfo, info)
			}

			if info.EmailVerified != test.wantUserInfo.EmailVerified {
				t.Errorf("expected UserInfo.EmailVerified to be %v , got %v", test.wantUserInfo.EmailVerified, info.EmailVerified)
			}
		})
	}

}

type testIssuer struct {
	baseURL string
	algs    []string
	jwks    *jose.JSONWebKeySet
}

func (t *testIssuer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		disc := struct {
			Issuer  string   `json:"issuer"`
			JWKSURI string   `json:"jwks_uri"`
			Algs    []string `json:"id_token_signing_alg_values_supported"`
		}{
			Issuer:  t.baseURL,
			JWKSURI: t.baseURL + "/keys",
			Algs:    t.algs,
		}
		if err := json.NewEncoder(w).Encode(disc); err != nil {
			panic("encoding discover doc: " + err.Error())
		}
	case "/keys":
		if err := json.NewEncoder(w).Encode(t.jwks); err != nil {
			panic("encoding keys: " + err.Error())
		}
	default:
		http.NotFound(w, r)
	}
}

func TestVerifierAlg(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating random key: %v", err)
	}
	pub := priv.Public()
	pubKey := jose.JSONWebKey{
		Algorithm: "ES256",
		Key:       pub,
		Use:       "sign",
	}

	signingKey := jose.SigningKey{
		Algorithm: jose.ES256,
		Key:       priv,
	}

	ts := &testIssuer{
		algs: []string{"ES256"},
		jwks: &jose.JSONWebKeySet{
			Keys: []jose.JSONWebKey{
				pubKey,
			},
		},
	}
	srv := httptest.NewServer(ts)
	ts.baseURL = srv.URL

	ctx := context.Background()

	provider, err := NewProvider(ctx, srv.URL)
	if err != nil {
		t.Fatalf("creating provider: %v", err)
	}

	now := time.Now()

	claims := struct {
		Iss string `json:"iss"`
		Sub string `json:"sub"`
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Iat int64  `json:"iat"`
	}{
		Iss: srv.URL,
		Sub: "test-user",
		Aud: "test-client",
		Exp: now.Add(time.Hour).Unix(),
		Iat: now.Add(-time.Hour).Unix(),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshaling claims: %v", err)
	}
	signer, err := jose.NewSigner(signingKey, nil)
	if err != nil {
		t.Fatalf("creating signing key: %v", err)
	}
	jws, err := signer.Sign(payload)
	if err != nil {
		t.Fatalf("signing token: %v", err)
	}
	rawIDToken, err := jws.CompactSerialize()
	if err != nil {
		t.Fatalf("serializing token: %v", err)
	}

	config := &Config{
		ClientID: "test-client",
	}
	verifier := provider.Verifier(config)
	idToken, err := verifier.Verify(ctx, rawIDToken)
	if err != nil {
		t.Fatalf("verifying token: %v", err)
	}
	if idToken.Subject != "test-user" {
		t.Errorf("expected subject 'test-user', got: %s", idToken.Subject)
	}

}
