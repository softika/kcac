package output_test

import (
	"testing"

	"github.com/softika/kcac/internal/model"
	"github.com/softika/kcac/internal/output"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		user model.UserRepresentation
		want output.AccountClass
	}{
		{
			name: "ordinary account",
			user: model.UserRepresentation{ID: "u1", Username: "a.gruber"},
			want: output.AccountHuman,
		},
		{
			name: "service account via the authoritative link",
			user: model.UserRepresentation{ID: "u2", Username: "service-account-kcac-audit", ServiceAccountClientLink: "c1"},
			want: output.AccountServiceAccount,
		},
		{
			name: "service account by naming convention when the link is absent",
			user: model.UserRepresentation{ID: "u3", Username: "service-account-legacy"},
			want: output.AccountServiceAccount,
		},
		{
			name: "federated shadow account",
			user: model.UserRepresentation{ID: "u4", Username: "ldap.user", FederationLink: "ldap-provider"},
			want: output.AccountFederated,
		},
		{
			name: "the authoritative link wins over the naming convention",
			user: model.UserRepresentation{ID: "u5", Username: "service-account-x", FederationLink: "ldap-provider"},
			want: output.AccountFederated,
		},
		{
			name: "a human who happens to be named like a service account is still labelled one",
			// Documented consequence of heuristic 3: the convention is strong
			// enough to act on, and a mislabel here is visible to a reviewer.
			user: model.UserRepresentation{ID: "u6", Username: "service-account-but-a-person"},
			want: output.AccountServiceAccount,
		},
		{
			name: "empty record is unknown, not human",
			user: model.UserRepresentation{},
			want: output.AccountUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := output.Classify(tt.user); got != tt.want {
				t.Errorf("Classify() = %q, want %q", got, tt.want)
			}
		})
	}
}
