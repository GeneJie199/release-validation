package guard

import "fmt"

func buildGuidance(report Report) []Guidance {
	items := []Guidance{}
	appendItem := func(item Guidance) {
		if len(items) < 50 {
			items = append(items, item)
		}
	}
	if coverage := report.Manifest.Changes; coverage != nil {
		for _, correlation := range coverage.Correlations {
			if correlation.Status == "matched" || correlation.Status == "optional-missing" {
				continue
			}
			appendItem(Guidance{Code: "expected-change." + correlation.Status, Priority: "blocking", Title: "Resolve unmatched expected change", Summary: fmt.Sprintf("Expected change %s is %s. Verify the deployment evidence or correct the versioned declaration, then create a new report.", correlation.ExpectedID, correlation.Status), RelatedIDs: []string{correlation.ExpectedID}})
		}
		for _, unexpected := range coverage.Unexpected {
			appendItem(Guidance{Code: "observed-change.unexpected", Priority: "blocking", Title: "Review unexpected observed change", Summary: fmt.Sprintf("%s %s %s was observed without an exact declaration. Investigate it and either remove the drift or add a reviewed declaration before rerunning.", unexpected.Source, unexpected.Action, unexpected.ResourceID), RelatedIDs: []string{unexpected.ID}})
		}
	}
	for _, result := range report.Results {
		if result.Status != "fail" || result.Type == "change_coverage" {
			continue
		}
		priority := "review"
		if result.Required {
			priority = "blocking"
		}
		appendItem(Guidance{Code: "check." + result.Type, Priority: priority, Title: "Resolve failed " + result.Phase + " check", Summary: result.Name + ": " + result.Summary, RelatedIDs: []string{result.Name}})
	}
	if report.Decision == "GO" {
		appendItem(Guidance{Code: "approval.review", Priority: "next", Title: "Complete the human release decision", Summary: "Review the exact report digest, expected-change coverage, observation evidence, and rollback prerequisites before recording the immutable approval."})
	} else if report.Decision == "HOLD" && len(items) == 0 {
		appendItem(Guidance{Code: "optional.review", Priority: "review", Title: "Review optional failures", Summary: "Decide whether the optional failures are acceptable. Human approval may preserve or tighten the automated decision, never relax it."})
	}
	return items
}
