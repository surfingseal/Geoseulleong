package odsay

import (
	"sort"
	"strings"
)

// Identical full observation recipes/uncertainty classes may be compared.
// Unknown and partial coverage never become a "low risk" cohort.
func comparisonGroup(c *Candidate, b *BoardingScore, w BoardingWeights) string {
	if b.Coverage != "full" {
		return ""
	}
	parts := []string{}
	for _, r := range b.Legs {
		sources := []string{}
		if r.History != nil && w.History > 0 {
			quality := "history"
			if r.LowSample {
				quality += "_low_sample"
			}
			sources = append(sources, quality)
		}
		if r.Seat != nil && w.Seats > 0 {
			sources = append(sources, "realtime")
		}
		if len(sources) == 0 {
			return ""
		}
		parts = append(parts, c.Legs[r.Leg].Kind+":"+strings.Join(sources, "+"))
	}
	return strings.Join(parts, "|")
}

// RankBoarding does not calculate risk. It preserves all original time ranks
// when any candidate lacks a comparable full observation recipe. Group ranks
// provide explicit local comparison without moving paths across unknown peers.
func RankBoarding(out *Candidates) {
	groups := map[string][]int{}
	globalComparable := len(out.Paths) > 0
	key := ""
	for i := range out.Paths {
		c := &out.Paths[i]
		b := c.Evaluation.Boarding
		b.GroupRank = nil
		b.GroupSize = 0
		b.RankChange = 0
		c.RecommendationRank = b.BaselineRank
		if b.ComparisonGroup == "" {
			globalComparable = false
		} else {
			groups[b.ComparisonGroup] = append(groups[b.ComparisonGroup], i)
		}
		if i == 0 {
			key = b.ComparisonGroup
		} else if key != b.ComparisonGroup {
			globalComparable = false
		}
	}
	for _, indices := range groups {
		sort.SliceStable(indices, func(i, j int) bool {
			return out.Paths[indices[i]].Evaluation.Boarding.Score < out.Paths[indices[j]].Evaluation.Boarding.Score
		})
		rank := 0
		for i, ix := range indices {
			b := out.Paths[ix].Evaluation.Boarding
			if i == 0 || b.Score != out.Paths[indices[i-1]].Evaluation.Boarding.Score {
				rank = i + 1
			}
			value := rank
			b.GroupRank = &value
			b.GroupSize = len(indices)
			if globalComparable {
				out.Paths[ix].RecommendationRank = rank
			}
		}
	}
	for i := range out.Paths {
		c := &out.Paths[i]
		b := c.Evaluation.Boarding
		b.ScoreComparable = globalComparable
		if globalComparable {
			b.RankingPolicy = "risk_score_full_comparable_coverage"
			b.Reasons = append(b.Reasons, "all_candidates_have_matching_coverage_recipe")
		} else {
			b.RankingPolicy = "travel_time_fallback_incomparable_coverage"
			b.Reasons = append(b.Reasons, "incomparable_coverage_preserve_all_travel_time_ranks")
			if b.GroupRank != nil {
				b.Reasons = append(b.Reasons, "risk_comparison_available_within_group_only")
			}
		}
		b.RankChange = b.BaselineRank - c.RecommendationRank
	}
	out.Policy["recommendation"] = "coverage_matched_risk_ranking_else_travel_time_v1"
	out.Policy["congestion_scoring"] = "heuristic_not_probability;unknown_never_treated_as_safe"
	out.Policy["coverage_comparison"] = "global_risk_rank_only_when_all_recipes_match;otherwise_time_rank_and_separate_group_rank"
}
