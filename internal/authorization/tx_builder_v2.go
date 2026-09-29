package authorization

import (
	"bytes"
	"context"
	"fmt"
	"reflect"

	sdkmath "cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/client"
	clienttx "github.com/cosmos/cosmos-sdk/client/tx"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/cosmos/cosmos-sdk/crypto/types/multisig"
	sdk "github.com/cosmos/cosmos-sdk/types"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/cosmos/gogoproto/proto"
	googleproto "google.golang.org/protobuf/proto"
	sdkv2 "spaghetti/internal/authorization/pb/sdkv2"
	v2pb "spaghetti/internal/authorization/pb/v2"
)

// CosmosAccountSignerV2 is a client-side keyring capability, never a gateway
// dependency. The keyring keeps private key material inside its backend.
type CosmosAccountSignerV2 struct {
	Keyring keyring.Keyring
	Name    string
}

type SignedTransactionV2 struct {
	TxBytes           []byte
	Tx                sdk.Tx
	CertificateDigest [32]byte
	Subject           string
	Sequence          uint64
}

// NewCosmosTxConfigV2 provides the same SDK protobuf transaction machinery as
// Alpha, with the exact V2 critical option registered for decoding.
func NewCosmosTxConfigV2() (client.TxConfig, error) {
	options, err := authtx.NewDefaultSigningOptions()
	if err != nil {
		return nil, err
	}
	registry, err := codectypes.NewInterfaceRegistryWithOptions(codectypes.InterfaceRegistryOptions{
		ProtoFiles: proto.HybridResolver, SigningOptions: *options,
	})
	if err != nil {
		return nil, err
	}
	sdk.RegisterInterfaces(registry)
	txtypes.RegisterInterfaces(registry)
	cryptocodec.RegisterInterfaces(registry)
	banktypes.RegisterInterfaces(registry)
	registry.RegisterImplementations((*txtypes.TxExtensionOptionI)(nil), &sdkv2.AuthorizationCertificateV2{})
	return authtx.NewTxConfig(codec.NewProtoCodec(registry), []signing.SignMode{signing.SignMode_SIGN_MODE_DIRECT}), nil
}

// BuildSignedV2Transaction builds exactly one SDK transaction. A stale account
// sequence requires new issuance; this function never adjusts or reuses the
// certificate with another sequence.
func BuildSignedV2Transaction(ctx context.Context, txConfig client.TxConfig, result CertificateIssueResultV2, accountSigner CosmosAccountSignerV2) (SignedTransactionV2, error) {
	if ctx == nil || txConfig == nil || accountSigner.Keyring == nil || accountSigner.Name == "" {
		return SignedTransactionV2{}, fmt.Errorf("missing V2 transaction dependency")
	}
	if err := ctx.Err(); err != nil {
		return SignedTransactionV2{}, err
	}
	intent := result.Intent
	if intent.ChainID != result.AccountState.ChainID || intent.AccountNumber != result.AccountState.AccountNumber || intent.Sequence != result.AccountState.Sequence {
		return SignedTransactionV2{}, fmt.Errorf("issued intent differs from trusted account state")
	}
	canonical, err := CanonicalizeCertificateSignDocV2(result.Certificate.SignDoc)
	if err != nil || !reflect.DeepEqual(canonical.Intent, intent) {
		return SignedTransactionV2{}, fmt.Errorf("issued certificate intent mismatch")
	}
	_, digest, err := CanonicalCertificateSignBytesV2(canonical)
	if err != nil || digest != result.Digest {
		return SignedTransactionV2{}, fmt.Errorf("issued certificate digest mismatch")
	}
	certificateBytes, err := MarshalAuthorizationCertificateV2(result.Certificate)
	if err != nil || !bytes.Equal(certificateBytes, result.CertificateBytes) {
		return SignedTransactionV2{}, fmt.Errorf("issued certificate bytes mismatch")
	}
	record, err := accountSigner.Keyring.Key(accountSigner.Name)
	if err != nil {
		return SignedTransactionV2{}, fmt.Errorf("account signer unavailable: %w", err)
	}
	address, err := record.GetAddress()
	if err != nil || address.String() != intent.Subject {
		return SignedTransactionV2{}, fmt.Errorf("account signer does not match certificate subject")
	}
	publicKey, err := record.GetPubKey()
	if err != nil || publicKey == nil {
		return SignedTransactionV2{}, fmt.Errorf("account signer public key unavailable")
	}
	if _, isMultisig := publicKey.(multisig.PubKey); isMultisig {
		return SignedTransactionV2{}, fmt.Errorf("V2 does not support multisig accounts")
	}
	amount, ok := sdkmath.NewIntFromString(intent.Amount)
	if !ok || !amount.IsPositive() {
		return SignedTransactionV2{}, fmt.Errorf("invalid issued transfer amount")
	}
	fees := make(sdk.Coins, len(intent.FeeAmount))
	for i, fee := range intent.FeeAmount {
		value, ok := sdkmath.NewIntFromString(fee.Amount)
		if !ok || value.IsNegative() {
			return SignedTransactionV2{}, fmt.Errorf("invalid issued fee amount")
		}
		fees[i] = sdk.NewCoin(fee.Denom, value)
	}
	builder := txConfig.NewTxBuilder()
	if err := builder.SetMsgs(&banktypes.MsgSend{
		FromAddress: intent.Subject, ToAddress: intent.Receiver,
		Amount: sdk.Coins{sdk.NewCoin(intent.Denom, amount)},
	}); err != nil {
		return SignedTransactionV2{}, err
	}
	builder.SetFeeAmount(fees)
	builder.SetGasLimit(intent.GasLimit)
	builder.SetMemo(intent.Memo)
	builder.SetTimeoutHeight(intent.TimeoutHeight)
	extended, ok := builder.(client.ExtendedTxBuilder)
	if !ok {
		return SignedTransactionV2{}, fmt.Errorf("SDK transaction builder lacks critical extension support")
	}
	extended.SetExtensionOptions(&codectypes.Any{TypeUrl: CertificateTypeURLV2, Value: certificateBytes})
	factory := clienttx.Factory{}.WithTxConfig(txConfig).WithKeybase(accountSigner.Keyring).
		WithChainID(intent.ChainID).WithAccountNumber(intent.AccountNumber).
		WithSequence(intent.Sequence).WithSignMode(signing.SignMode_SIGN_MODE_DIRECT)
	if err := clienttx.Sign(ctx, factory, accountSigner.Name, builder, true); err != nil {
		return SignedTransactionV2{}, fmt.Errorf("sign V2 transaction: %w", err)
	}
	encoded, err := txConfig.TxEncoder()(builder.GetTx())
	if err != nil {
		return SignedTransactionV2{}, fmt.Errorf("encode V2 transaction: %w", err)
	}
	decoded, err := txConfig.TxDecoder()(encoded)
	if err != nil {
		return SignedTransactionV2{}, fmt.Errorf("decode signed V2 transaction: %w", err)
	}
	if err := checkSignedV2RoundTrip(decoded, encoded, intent, certificateBytes); err != nil {
		return SignedTransactionV2{}, err
	}
	return SignedTransactionV2{TxBytes: encoded, Tx: decoded, CertificateDigest: result.Digest, Subject: intent.Subject, Sequence: intent.Sequence}, nil
}

func checkSignedV2RoundTrip(decoded sdk.Tx, encoded []byte, intent AuthorizationIntentV2, certificateBytes []byte) error {
	var raw txtypes.TxRaw
	if err := proto.Unmarshal(encoded, &raw); err != nil || len(raw.Signatures) != 1 {
		return fmt.Errorf("invalid signed SDK TxRaw")
	}
	var body txtypes.TxBody
	var authInfo txtypes.AuthInfo
	if err := proto.Unmarshal(raw.BodyBytes, &body); err != nil {
		return fmt.Errorf("invalid signed SDK body: %w", err)
	}
	if err := proto.Unmarshal(raw.AuthInfoBytes, &authInfo); err != nil {
		return fmt.Errorf("invalid signed SDK auth info: %w", err)
	}
	if body.Unordered || body.TimeoutTimestamp != nil || len(body.Messages) != 1 || len(body.ExtensionOptions) != 1 ||
		len(body.NonCriticalExtensionOptions) != 0 || authInfo.Tip != nil || authInfo.Fee == nil ||
		authInfo.Fee.Payer != "" || authInfo.Fee.Granter != "" || len(authInfo.SignerInfos) != 1 ||
		authInfo.SignerInfos[0].Sequence != intent.Sequence || authInfo.SignerInfos[0].ModeInfo == nil ||
		authInfo.SignerInfos[0].ModeInfo.GetSingle() == nil ||
		authInfo.SignerInfos[0].ModeInfo.GetSingle().Mode != signing.SignMode_SIGN_MODE_DIRECT {
		return fmt.Errorf("unsupported signed V2 transaction shape")
	}
	tx, ok := decoded.(interface {
		sdk.Tx
		GetExtensionOptions() []*codectypes.Any
		GetNonCriticalExtensionOptions() []*codectypes.Any
		GetMemo() string
		GetTimeoutHeight() uint64
		GetFee() sdk.Coins
		GetGas() uint64
		GetSignaturesV2() ([]signing.SignatureV2, error)
	})
	if !ok {
		return fmt.Errorf("unexpected SDK transaction type")
	}
	msgs, options := tx.GetMsgs(), tx.GetExtensionOptions()
	if len(msgs) != 1 || len(options) != 1 || len(tx.GetNonCriticalExtensionOptions()) != 0 ||
		options[0] == nil || options[0].TypeUrl != CertificateTypeURLV2 || !bytes.Equal(options[0].Value, certificateBytes) ||
		tx.GetMemo() != intent.Memo || tx.GetTimeoutHeight() != intent.TimeoutHeight || tx.GetGas() != intent.GasLimit ||
		len(tx.GetFee()) != len(intent.FeeAmount) {
		return fmt.Errorf("signed V2 transaction round-trip mismatch")
	}
	msg, ok := msgs[0].(*banktypes.MsgSend)
	if !ok || msg.FromAddress != intent.Subject || msg.ToAddress != intent.Receiver || len(msg.Amount) != 1 ||
		msg.Amount[0].Denom != intent.Denom || msg.Amount[0].Amount.String() != intent.Amount {
		return fmt.Errorf("signed V2 MsgSend mismatch")
	}
	for i, fee := range tx.GetFee() {
		if fee.Denom != intent.FeeAmount[i].Denom || fee.Amount.String() != intent.FeeAmount[i].Amount {
			return fmt.Errorf("signed V2 fee mismatch")
		}
	}
	sigs, err := tx.GetSignaturesV2()
	if err != nil || len(sigs) != 1 || sigs[0].Sequence != intent.Sequence {
		return fmt.Errorf("signed V2 signer mismatch")
	}
	direct, ok := sigs[0].Data.(*signing.SingleSignatureData)
	if !ok || direct.SignMode != signing.SignMode_SIGN_MODE_DIRECT || len(direct.Signature) == 0 {
		return fmt.Errorf("signed V2 mode mismatch")
	}
	var certificate v2pb.AuthorizationCertificateV2
	if err := googleproto.Unmarshal(options[0].Value, &certificate); err != nil || certificate.SignDoc == nil || certificate.SignDoc.Intent == nil {
		return fmt.Errorf("signed V2 certificate mismatch")
	}
	return nil
}
