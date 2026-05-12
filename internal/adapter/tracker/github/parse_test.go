package github

import "testing"

func TestParseGitHubIssueID(t *testing.T) {
	cases := []struct {
		in       string
		wantRepo string
		wantNum  int
		wantErr  bool
	}{
		{"nSimonFR/nic-os#60", "nSimonFR/nic-os", 60, false},
		{"a/b#1", "a/b", 1, false},
		{"owner/repo-with-dashes#42", "owner/repo-with-dashes", 42, false},
		{"invalid", "", 0, true},
		{"missing-num#", "", 0, true},
		{"#42", "", 42, false}, // pathological but parseable; caller's repos won't have this
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			repo, num, err := parseGitHubIssueID(tc.in)
			gotErr := err != nil
			if gotErr != tc.wantErr {
				t.Fatalf("err=%v want err=%v (msg=%v)", gotErr, tc.wantErr, err)
			}
			if !tc.wantErr {
				if repo != tc.wantRepo {
					t.Errorf("repo = %q, want %q", repo, tc.wantRepo)
				}
				if num != tc.wantNum {
					t.Errorf("num = %d, want %d", num, tc.wantNum)
				}
			}
		})
	}
}
