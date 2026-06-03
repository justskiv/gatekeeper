package engine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
			assert.Equal(t, tt.want, status, "status")
			assert.Equal(t, tt.want, decision.Status, "decision status")
			assert.Equal(t, tt.want == domain.StatusActive, decision.Allowed,
				"allowed")
			require.NotEmpty(t, decision.Reasons, "decision has no reasons")

			if tt.banned {
				assert.Len(t, decision.Reasons, len(tt.verdicts)+1,
					"source reasons plus ban")
			}
		})
	}
}
