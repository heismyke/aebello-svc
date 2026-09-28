package fx

import "testing"

func TestCharge(t *testing.T) {
	c := New("NGN", 0.03, 1327.30)
	got, err := c.Charge(599) // $5.99
	if err != nil {
		t.Fatal(err)
	}
	// 5.99 x 1327.30 x 1.03 = ₦8,188.9 -> ₦8,190, in kobo
	if got.Amount != 819000 || got.Currency != "NGN" {
		t.Errorf("Charge($5.99) = %+v, want ₦8,190 (819000 kobo)", got)
	}
}

func TestChargeUSD(t *testing.T) {
	got, _ := New("USD", 0.03, 0).Charge(599)
	if got.Amount != 599 || got.Currency != "USD" {
		t.Errorf("USD charge = %+v, want unchanged", got)
	}
}

func TestChargeWithoutRate(t *testing.T) {
	if _, err := New("NGN", 0.03, 0).Charge(599); err == nil {
		t.Error("expected an error before any rate is known")
	}
}
