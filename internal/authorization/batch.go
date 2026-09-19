package authorization

type TrustedBatchContext struct {
	ChainID       string
	BatchID       uint64
	PolicyID      string
	PolicyVersion uint64
	PolicyHash    []byte
	IssuerSetID   uint64
}

// BuildBatchSignDoc builds a detached canonical sign document exclusively from
// trusted batch metadata and validated authorization records.
func BuildBatchSignDoc(trusted TrustedBatchContext, records []AuthorizationRecord) (BatchSignDoc, error) {
	input := BatchSignDoc{
		Domain:        BatchDomain,
		ChainID:       trusted.ChainID,
		BatchID:       trusted.BatchID,
		PolicyID:      trusted.PolicyID,
		PolicyVersion: trusted.PolicyVersion,
		PolicyHash:    append([]byte(nil), trusted.PolicyHash...),
		IssuerSetID:   trusted.IssuerSetID,
		Records:       append([]AuthorizationRecord(nil), records...),
	}
	return CanonicalizeBatchSignDoc(input)
}
