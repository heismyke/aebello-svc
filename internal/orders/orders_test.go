package orders

import "testing"

func TestFriendlyStatus(t *testing.T) {
	cases := []struct {
		esim, smdp string
		used       int64
		want       string
	}{
		{"GOT_RESOURCE", "RELEASED", 0, "ready"},
		{"IN_USE", "ENABLED", 0, "installed"},
		{"GOT_RESOURCE", "ENABLED", 0, "installed"},
		{"IN_USE", "ENABLED", 123, "active"},
		{"IN_USE", "DISABLED", 123, "active"},
		{"USED_UP", "ENABLED", 999, "depleted"},
		{"IN_USE", "DELETED", 999, "deleted"},
		{"USED_EXPIRED", "ENABLED", 10, "expired"},
		{"CANCEL", "RELEASED", 0, "cancelled"},
	}
	for _, c := range cases {
		if got := friendlyStatus(c.esim, c.smdp, c.used); got != c.want {
			t.Errorf("friendlyStatus(%s, %s, %d) = %s, want %s", c.esim, c.smdp, c.used, got, c.want)
		}
	}
}
