package authorization

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type testBearerV2 struct {
	authenticated, bound bool
	err                  error
	calls                int
}

func (v *testBearerV2) ValidateTokenSubject(_ context.Context, token, subject string) (bool, bool, error) {
	v.calls++
	if token != "valid" {
		return false, false, nil
	}
	return v.authenticated, v.bound, v.err
}

func TestCertificateHTTPBoundary(t *testing.T) {
	issuer, request, _, evaluator, state, signer := issuerV2Fixture(t)
	valid := &testBearerV2{authenticated: true, bound: true}
	form := func(subject string) string {
		value, _ := json.Marshal(map[string]any{
			"subject": subject, "receiver": request.Receiver, "denom": request.Denom,
			"amount": request.Amount, "timeout_height": "123", "memo": request.Memo,
			"fee_amount": []map[string]string{{"denom": request.FeeAmount[0].Denom, "amount": request.FeeAmount[0].Amount}},
			"gas_limit":  "250000",
		})
		return string(value)
	}
	call := func(body, token string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/api/v2/certificates", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		NewCertificateHTTPHandler(issuer, valid).ServeHTTP(w, r)
		return w
	}
	w := call(form(request.Subject), "valid")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("ALLOW: %d %s", w.Code, w.Body.String())
	}
	var response struct {
		ProtocolVersion   string         `json:"protocol_version"`
		CertificateBytes  string         `json:"certificate_bytes_base64"`
		CertificateDigest string         `json:"certificate_digest"`
		Intent            map[string]any `json:"intent"`
		AccountNumber     string         `json:"account_number"`
		Sequence          string         `json:"sequence"`
		ChainID           string         `json:"chain_id"`
		ValidFromHeight   string         `json:"valid_from_height"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	encoded, err := base64.StdEncoding.DecodeString(response.CertificateBytes)
	if err != nil || len(encoded) == 0 || response.ProtocolVersion != "authz-protocol-v2.0.0" || len(response.CertificateDigest) != 64 ||
		response.AccountNumber != "7" || response.Sequence != "3" || response.ChainID != "alpha-1" || response.ValidFromHeight != "100" ||
		response.Intent["account_number"] != "7" || response.Intent["sequence"] != "3" || response.Intent["chain_id"] != "alpha-1" ||
		state.calls != 1 || len(signer.message) == 0 {
		t.Fatalf("invalid authoritative response: %s", w.Body.String())
	}
	for _, secret := range []string{"private-keycloak-client-secret", "private-issuer-seed", "sign_bytes", "raw_attributes"} {
		if bytes.Contains(w.Body.Bytes(), []byte(secret)) {
			t.Fatalf("secret in response: %s", secret)
		}
	}
	if got := call(form(request.Subject), ""); got.Code != 401 {
		t.Fatalf("missing token: %d", got.Code)
	}
	if got := call(form(request.Subject), "invalid"); got.Code != 401 {
		t.Fatalf("invalid token: %d", got.Code)
	}
	valid.bound = false
	if got := call(form(request.Subject), "valid"); got.Code != 403 {
		t.Fatalf("subject mismatch: %d", got.Code)
	}
	valid.bound = true
	for _, malformed := range []string{
		`{"subject":"wrong","receiver":"wrong","denom":"token","amount":"1","timeout_height":"0","fee_amount":[],"gas_limit":"1"}`,
		strings.Replace(form(request.Subject), `"amount":"`+request.Amount+`"`, `"amount":"-1"`, 1),
		strings.Replace(form(request.Subject), `"amount":"`+request.FeeAmount[0].Amount+`"`, `"amount":"-1"`, 1),
		strings.Replace(form(request.Subject), `"gas_limit":"250000"`, `"gas_limit":"01"`, 1),
		strings.Replace(form(request.Subject), `"gas_limit":"250000"`, `"gas_limit":"18446744073709551616"`, 1),
		strings.Replace(form(request.Subject), `"gas_limit":"250000"`, `"gas_limit":"250000","policy_id":"evil"`, 1),
		strings.Replace(form(request.Subject), `"gas_limit":"250000"`, `"gas_limit":"250000","messages":[]`, 1),
	} {
		if got := call(malformed, "valid"); got.Code != 400 {
			t.Fatalf("malformed accepted: %d %s", got.Code, malformed)
		}
	}
	evaluator.decision.Allow = false
	evaluator.decision.ReasonCode = "AUTHZ_V2_POLICY_DENIED"
	if got := call(form(request.Subject), "valid"); got.Code != 403 || !strings.Contains(got.Body.String(), "AUTHZ_V2_POLICY_DENIED") {
		t.Fatalf("deny: %d %s", got.Code, got.Body.String())
	}
	evaluator.decision.Allow = true
	state.err = errors.New("private-issuer-seed")
	if got := call(form(request.Subject), "valid"); got.Code != 503 || strings.Contains(got.Body.String(), "private-issuer-seed") || !strings.Contains(got.Body.String(), "ISSUANCE_FAILED") {
		t.Fatalf("failure leak: %d %s", got.Code, got.Body.String())
	}
}
