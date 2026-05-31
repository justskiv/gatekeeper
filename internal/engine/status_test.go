package engine

import (
	"testing"

	"github.com/justskiv/gatekeeper/internal/domain"
)

func TestAggregatePriorities(t *testing.T) {
	tests := []struct {
		name     string
		banned   bool
		verdicts []domain.Verdict
		want     domain.EffectiveStatus
	}{
		{
			name:     "active wins",
			verdicts: []domain.Verdict{domain.VerdictInactive, domain.VerdictActive},
			want:     domain.StatusActive,
		},
		{
			name:     "unknown blocks inactive",
			verdicts: []domain.Verdict{domain.VerdictInactive, domain.VerdictUnknown},
			want:     domain.StatusUnknown,
		},
		{
			name:     "all no signal",
			verdicts: []domain.Verdict{domain.VerdictNoSignal, domain.VerdictNoSignal},
			want:     domain.StatusInactive,
		},
		{
			name:     "inactive without unknown",
			verdicts: []domain.Verdict{domain.VerdictNoSignal, domain.VerdictInactive},
			want:     domain.StatusInactive,
		},
		{
			name:     "hard ban overrides active",
			banned:   true,
			verdicts: []domain.Verdict{domain.VerdictActive},
			want:     domain.StatusInactive,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verdicts := make([]domain.SourceVerdict, 0, len(tt.verdicts))
			for _, verdict := range tt.verdicts {
				verdicts = append(verdicts, domain.SourceVerdict{
					Source:  domain.PlatformBoosty,
					Verdict: verdict,
					Detail:  string(verdict),
				})
			}

			status, decision := Aggregate(verdicts, tt.banned)
			if status != tt.want || decision.Status != tt.want {
				t.Fatalf("status = %s/%s, want %s",
					status, decision.Status, tt.want)
			}

			if decision.Allowed != (tt.want == domain.StatusActive) {
				t.Fatalf("allowed = %v for status %s", decision.Allowed, tt.want)
			}

			if len(decision.Reasons) == 0 {
				t.Fatal("decision has no reasons")
			}

			if tt.banned && len(decision.Reasons) != len(tt.verdicts)+1 {
				t.Fatalf("reasons = %+v, want source reasons plus ban", decision.Reasons)
			}
		})
	}
}
