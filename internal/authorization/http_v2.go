package authorization

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
)

// BearerSubjectValidator checks a Keycloak access token and its server-side wallet binding.
type BearerSubjectValidator interface {
	ValidateTokenSubject(context.Context, string, string) (bool, bool, error)
}

type certificateIssuerHTTP interface {
	Issue(context.Context, CertificateIssueRequestV2) (CertificateIssueResultV2, error)
}

type certificateRequestHTTP struct {
	Subject       string        `json:"subject"`
	Receiver      string        `json:"receiver"`
	Denom         string        `json:"denom"`
	Amount        string        `json:"amount"`
	TimeoutHeight string        `json:"timeout_height"`
	Memo          string        `json:"memo"`
	FeeAmount     []FeeCoinHTTP `json:"fee_amount"`
	GasLimit      string        `json:"gas_limit"`
}

type FeeCoinHTTP struct {
	Denom  string `json:"denom"`
	Amount string `json:"amount"`
}

func certificateHTTPError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{code, message})
}

// NewCertificateHTTPHandler exposes only V2 certificate issuance; caller serves it over HTTPS.
func NewCertificateHTTPHandler(issuer certificateIssuerHTTP, validator BearerSubjectValidator) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost {
			certificateHTTPError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "POST required")
			return
		}
		authorization := r.Header.Get("Authorization")
		if !strings.HasPrefix(authorization, "Bearer ") || strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer ")) == "" || strings.ContainsAny(strings.TrimPrefix(authorization, "Bearer "), " \t\r\n") {
			certificateHTTPError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Bearer token required")
			return
		}
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			certificateHTTPError(w, http.StatusUnsupportedMediaType, "INVALID_CONTENT_TYPE", "JSON required")
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
		if err != nil {
			certificateHTTPError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request")
			return
		}
		if _, err = strictJSONObject(body); err != nil {
			certificateHTTPError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request")
			return
		}
		var input certificateRequestHTTP
		decoder := json.NewDecoder(strings.NewReader(string(body)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil {
			certificateHTTPError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request")
			return
		}
		timeout, err := strconv.ParseUint(input.TimeoutHeight, 10, 64)
		if err != nil || strconv.FormatUint(timeout, 10) != input.TimeoutHeight {
			certificateHTTPError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request")
			return
		}
		gas, err := strconv.ParseUint(input.GasLimit, 10, 64)
		if err != nil || strconv.FormatUint(gas, 10) != input.GasLimit {
			certificateHTTPError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request")
			return
		}
		request := CertificateIssueRequestV2{Subject: input.Subject, Receiver: input.Receiver, Denom: input.Denom,
			Amount: input.Amount, TimeoutHeight: timeout, Memo: input.Memo, GasLimit: gas}
		for _, fee := range input.FeeAmount {
			request.FeeAmount = append(request.FeeAmount, FeeCoinV2{Denom: fee.Denom, Amount: fee.Amount})
		}
		if _, err := BuildCertificateSignDocV2(AuthorizationIntentV2{
			Subject: request.Subject, Receiver: request.Receiver, Denom: request.Denom, Amount: request.Amount,
			TimeoutHeight: request.TimeoutHeight, Memo: request.Memo, FeeAmount: request.FeeAmount, GasLimit: request.GasLimit,
		}, TrustedCertificateContextV2{ChainID: "validation", PolicyID: "validation", PolicyVersion: 1,
			PolicyHash: make([]byte, 32), IssuerSetID: 1, ValidFromHeight: 1, ValidUntilHeight: 1}); err != nil {
			certificateHTTPError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request")
			return
		}
		authenticated, bound, err := validator.ValidateTokenSubject(r.Context(), strings.TrimPrefix(authorization, "Bearer "), request.Subject)
		if err != nil {
			certificateHTTPError(w, http.StatusServiceUnavailable, "AUTH_UNAVAILABLE", "Authentication unavailable")
			return
		}
		if !authenticated {
			certificateHTTPError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Invalid bearer token")
			return
		}
		if !bound {
			certificateHTTPError(w, http.StatusForbidden, "SUBJECT_MISMATCH", "Subject not authorized")
			return
		}
		result, err := issuer.Issue(r.Context(), request)
		if err != nil {
			var denied *PolicyDeniedError
			if errors.As(err, &denied) {
				code := denied.ReasonCode
				if code == "" || len(code) > 80 || strings.IndexFunc(code, func(r rune) bool {
					return r != '_' && (r < 'A' || r > 'Z') && (r < '0' || r > '9')
				}) >= 0 {
					code = "POLICY_DENIED"
				}
				certificateHTTPError(w, http.StatusForbidden, code, "Policy denied")
			} else {
				certificateHTTPError(w, http.StatusServiceUnavailable, "ISSUANCE_FAILED", "Certificate issuance unavailable")
			}
			return
		}
		intent := result.Intent
		fees := make([]FeeCoinHTTP, 0, len(intent.FeeAmount))
		for _, fee := range intent.FeeAmount {
			fees = append(fees, FeeCoinHTTP{fee.Denom, fee.Amount})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			ProtocolVersion   string `json:"protocol_version"`
			CertificateBytes  string `json:"certificate_bytes_base64"`
			CertificateDigest string `json:"certificate_digest"`
			Intent            any    `json:"intent"`
			ValidFromHeight   string `json:"valid_from_height"`
			ValidUntilHeight  string `json:"valid_until_height"`
			AccountNumber     string `json:"account_number"`
			Sequence          string `json:"sequence"`
			ChainID           string `json:"chain_id"`
		}{"authz-protocol-v2.0.0", base64.StdEncoding.EncodeToString(result.CertificateBytes),
			hex.EncodeToString(result.Digest[:]), struct {
				ChainID       string        `json:"chain_id"`
				Subject       string        `json:"subject"`
				Receiver      string        `json:"receiver"`
				Denom         string        `json:"denom"`
				Amount        string        `json:"amount"`
				AccountNumber string        `json:"account_number"`
				Sequence      string        `json:"sequence"`
				TimeoutHeight string        `json:"timeout_height"`
				Memo          string        `json:"memo"`
				FeeAmount     []FeeCoinHTTP `json:"fee_amount"`
				GasLimit      string        `json:"gas_limit"`
			}{intent.ChainID, intent.Subject, intent.Receiver, intent.Denom, intent.Amount,
				strconv.FormatUint(intent.AccountNumber, 10), strconv.FormatUint(intent.Sequence, 10),
				strconv.FormatUint(intent.TimeoutHeight, 10), intent.Memo, fees, strconv.FormatUint(intent.GasLimit, 10)},
			strconv.FormatInt(result.Certificate.SignDoc.ValidFromHeight, 10),
			strconv.FormatInt(result.Certificate.SignDoc.ValidUntilHeight, 10),
			strconv.FormatUint(intent.AccountNumber, 10), strconv.FormatUint(intent.Sequence, 10), intent.ChainID})
	})
}
