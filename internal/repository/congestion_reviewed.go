package repository

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"geoseulleong/internal/congestionplan"
	"strings"
)

func (s AnalysisStore) SaveReviewedSelection(ctx context.Context, p congestionplan.Plan) (string, error) {
	if e := ValidateReviewedPlan(p); e != nil {
		return "", e
	}
	return s.saveSelection(ctx, p, true)
}
func ValidateReviewedPlan(p congestionplan.Plan) error {
	if p.Validate() != nil || len(p.Mappings) != 22 || p.RouteID != 1 || p.Date != "20261001" {
		return errors.New("reviewed plan scope invalid")
	}
	var proof struct {
		Version    string            `json:"version"`
		Snapshot   string            `json:"gbis_snapshot_sha256"`
		Hashes     map[string]string `json:"metadata_hashes"`
		Historical string            `json:"historical_mapping_status"`
		Direction  bool              `json:"direction_verified"`
		Exception  string            `json:"single_exception"`
	}
	if json.Unmarshal(p.MappingProvenance, &proof) != nil || proof.Version != "reviewed-v3" || proof.Snapshot != "69800d02640f96035ee8f634af74b62e7a76bfe6300da360405dd15627ff871e" || proof.Historical != "unverified" || proof.Direction || len(proof.Hashes) != 5 || proof.Exception != "7104180/277104180/41002905/234001317/23/24" {
		return errors.New("review proof invalid")
	}
	seen := map[int64]bool{}
	for _, m := range p.Mappings {
		v := m.Result.Visit
		if m.Result.Status != "verified" || v == nil || v.RouteID != p.RouteID || seen[v.ID] || len(m.Result.Candidates) != 1 || m.Result.Candidates[0] != *v || m.Source.Route != "41002905" || m.Source.BISID != v.ExternalID || !m.Evidence.BISNamespaceConfirmed || !m.Evidence.RouteConfirmed {
			return errors.New("reviewed link invalid or ambiguous")
		}
		seen[v.ID] = true
		validHash := false
		for _, h := range proof.Hashes {
			if h == m.Evidence.ResponseSHA {
				b, e := hex.DecodeString(h)
				validHash = e == nil && len(b) == 32
			}
		}
		if !validHash {
			return errors.New("metadata hash not in review proof")
		}
		if m.Source.ID == "7104180" {
			if m.Source.BISID != "277104180" || m.Source.Sequence != 23 || v.ID != 24 || v.Sequence != 24 || m.Source.Name != "한남1고가차도(미정차)" || v.Name != "한남1고가차도(경유)" || m.Result.Method != "user_approved_exact_hannam_reference_v3" {
				return errors.New("exact exception differs")
			}
		} else if strings.Join(strings.Fields(m.Source.Name), "") != strings.Join(strings.Fields(v.Name), "") {
			return errors.New("unapproved name difference")
		}
	}
	return nil
}
