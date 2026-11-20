package stopmapping

import "errors"

// PromoteHannam is an explicitly approved exception for one immutable reference snapshot.
// It never declares '(미정차)' and '(경유)' general synonyms.
func PromoteHannam(r MetadataReview, gbisSnapshotSHA string) (MetadataReview, error) {
	if gbisSnapshotSHA != "69800d02640f96035ee8f634af74b62e7a76bfe6300da360405dd15627ff871e" || r.Observation != (Observation{StopID: "7104180", Sequence: 23, Route: "41002905", Province: "11", District: "11170"}) || len(r.OfficialRouteRows) != 1 || len(r.OfficialStopRows) != 1 || len(r.Result.Candidates) != 1 || r.Result.Status != "candidate" || !r.IDCorrespondenceVerified || !r.RouteMembershipVerified || !r.RegionVerified || r.DirectionVerified || r.CoordinatesVerified || r.HistoricalValidity != "20261001_validity_not_verified" {
		return r, errors.New("approved Hannam promotion preconditions differ")
	}
	route, stop, v := r.OfficialRouteRows[0], r.OfficialStopRows[0], r.Result.Candidates[0]
	if route.Route != "41002905" || route.RouteNumber != "M5121" || route.ID != "7104180" || route.Sequence != 23 || route.Name != "한남1고가차도(미정차)" || stop.ID != "7104180" || stop.BISID != "277104180" || stop.Name != "한남1고가차도(미정차)" || stop.Province != "11" || stop.District != "11170" || stop.Dong != "1117013100" || v.RouteID != 1 || v.ID != 24 || v.StopID != "gbis:277104180" || v.ExternalID != "277104180" || v.Sequence != 24 || v.Name != "한남1고가차도(경유)" || !v.Via {
		return r, errors.New("approved Hannam identity or unique visit differs")
	}
	r.Result.Status = "verified"
	r.Result.Method = "user_approved_exact_hannam_reference_v3"
	r.Result.Visit = &v
	r.Result.Reasons = []string{"explicitly_approved_qualifier_difference_for_this_reference_only"}
	return r, nil
}
